package httpapi

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"github.com/risulongmemory/archive-center-go/internal/store"
	"hash"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
	"unsafe"
)

// The fingerprint is only compared for equality. Two readings get the same
// value exactly when their JSON encodings are equal: every exported field is
// hashed length-prefixed (each part through its own digest), invalid UTF-8
// bytes are replaced one by one as JSON does, and nil and empty slices stay
// distinct (null versus []). This avoids marshalling readings that carry
// thousands of linked parts, and lets a request reuse the digest of a part
// shared by many readings. A new exported field on either struct must be
// added here.
func (c *prepareTurnMemoryContext) encodedFingerprint(partDigest func(prepareTurnMemoryPart) [32]byte) [32]byte {
	h := sha256.New()
	prepareTurnFingerprintString(h, c.Path)
	prepareTurnFingerprintString(h, c.Label)
	prepareTurnFingerprintString(h, c.DisplayPath)
	prepareTurnFingerprintSliceHeader(h, c.Parts == nil, len(c.Parts))
	for _, part := range c.Parts {
		digest := partDigest(part)
		h.Write(digest[:])
	}
	var out [32]byte
	h.Sum(out[:0])
	return out
}

func prepareTurnMemoryPartDigest(part prepareTurnMemoryPart) [32]byte {
	h := sha256.New()
	prepareTurnFingerprintString(h, part.Key)
	prepareTurnFingerprintString(h, part.Label)
	prepareTurnFingerprintString(h, part.Value)
	prepareTurnFingerprintString(h, part.DeliveryLabel)
	if part.ReferenceOnly {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
	}
	prepareTurnFingerprintSliceHeader(h, part.FactTexts == nil, len(part.FactTexts))
	for _, text := range part.FactTexts {
		prepareTurnFingerprintString(h, text)
	}
	var out [32]byte
	h.Sum(out[:0])
	return out
}

func prepareTurnFingerprintLen(h hash.Hash, n int) {
	var scratch [binary.MaxVarintLen64]byte
	h.Write(scratch[:binary.PutUvarint(scratch[:], uint64(n))])
}

func prepareTurnFingerprintString(h hash.Hash, s string) {
	if utf8.ValidString(s) {
		prepareTurnFingerprintLen(h, len(s))
		h.Write([]byte(s))
		return
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteRune(utf8.RuneError)
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	prepareTurnFingerprintLen(h, b.Len())
	h.Write([]byte(b.String()))
}

func prepareTurnFingerprintSliceHeader(h hash.Hash, isNil bool, n int) {
	if isNil {
		h.Write([]byte{0})
		return
	}
	h.Write([]byte{1})
	prepareTurnFingerprintLen(h, n)
}

// prepareTurnMemoryFormChars is utf8.RuneCountInString(text) for a form text,
// strings.TrimSpace(heading + "\n" + "  "-indented part texts joined by "\n"),
// computed from its pieces' counts: a trimmed, non-empty heading leaves only
// trailing spaces to trim. Invalid UTF-8, where joined bytes could decode
// differently, or an empty heading counts text itself.
func prepareTurnMemoryFormChars(heading string, parts []prepareTurnMemoryFormPart, text string, measure func(string) prepareTurnTextRunes) int {
	first := measurePrepareTurnText(heading)
	if !first.valid || first.runes == 0 {
		return utf8.RuneCountInString(text)
	}
	total, trailing, valid := 0, 0, true
	add := func(m prepareTurnTextRunes) {
		valid = valid && m.valid
		total += m.runes
		if m.trailingSpaces == m.runes {
			trailing += m.runes
		} else {
			trailing = m.trailingSpaces
		}
	}
	newline := prepareTurnTextRunes{runes: 1, trailingSpaces: 1, valid: true}
	indent := prepareTurnTextRunes{runes: 2, trailingSpaces: 2, valid: true}
	add(first)
	add(newline)
	for i, part := range parts {
		if i > 0 {
			add(newline)
		}
		add(indent)
		add(measure(part.Text))
	}
	if !valid {
		return utf8.RuneCountInString(text)
	}
	return total - trailing
}

// prepareTurnCurrentStatePlan is what attaching current states needs from the
// states alone: the public current views with their subject keys and names,
// and the parts each attaches (built when first attached). It depends only on
// the values, the entity aliases and the clock, so a request that attaches
// the same states to many seed sets builds it once.
type prepareTurnCurrentStatePlan struct {
	mu      sync.Mutex // guards the lazily built parts
	aliases map[string]any
	clock   map[string]any
	states  []prepareTurnPlannedState
	// A request attaches the plan to the first candidates and again to each
	// supplemental search's, which are mostly the same facts. What attaching
	// derives from a fact's own text and entity surface is kept here for
	// the plan's request, so each search does not derive it again.
	sourceMu   sync.Mutex
	sources    map[string]prepareTurnStateSource
	entityKeys map[string]string
}

// prepareTurnStateSource is a fact source text's recall terms and its
// lowercased words joined by single spaces, padded with one on each side.
type prepareTurnStateSource struct {
	terms  []string
	phrase string
}

type prepareTurnPlannedState struct {
	view          narrativeCurrentStateView
	subjectKey    string
	subjectWords  []string
	subjectPhrase string
	subjectKeys   prepareTurnSubjectKeys
	sourceFields  []string
	parts         []prepareTurnMemoryPart
	partsBuilt    bool
}

func newPrepareTurnCurrentStatePlan(values []store.StatusCurrentValue, aliases, clock map[string]any) *prepareTurnCurrentStatePlan {
	wordBreak := func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '_' && r != '-' }
	plan := &prepareTurnCurrentStatePlan{aliases: aliases, clock: clock}
	for _, view := range narrativeCurrentStateViews(values) {
		switch view.Scope {
		case "belief", "rumor", "secret":
			continue // Same public-current projection boundary as lifecycle readings.
		}
		if view.Slot == "goal_status" {
			continue // Commitments retain their explicit lifecycle-key owner.
		}
		subjectWords := strings.FieldsFunc(strings.ToLower(view.Subject), wordBreak)
		plan.states = append(plan.states, prepareTurnPlannedState{
			view:          view,
			subjectKey:    prepareTurnPriorityEntityKey(view.Subject, aliases),
			subjectWords:  subjectWords,
			subjectPhrase: " " + strings.Join(subjectWords, " ") + " ",
			subjectKeys:   newPrepareTurnSubjectKeys(view.Subject),
			sourceFields:  stringsFromAny(view.Payload["source_fields"]),
		})
	}
	return plan
}

// statePartsOf builds a planned state's attached parts on first use.
func (plan *prepareTurnCurrentStatePlan) statePartsOf(state *prepareTurnPlannedState) []prepareTurnMemoryPart {
	plan.mu.Lock()
	defer plan.mu.Unlock()
	if state.partsBuilt {
		return state.parts
	}
	view, clock := state.view, plan.clock
	origin, evidence := prepareTurnCurrentStateReadingOrigin(view.Value)
	prefix := fmt.Sprintf("@current/%s/%s/%s", view.Value.OwnerScope, view.Value.OwnerID, view.Slot)
	observation := "source turn unknown"
	if origin.SourceTurn > 0 {
		observation = fmt.Sprintf("source turn %d", origin.SourceTurn)
	}
	parts := []prepareTurnMemoryPart{{Key: prefix, Label: fmt.Sprintf("linked stored state [%s; status_current_values:%d]", observation, view.Value.ID), DeliveryLabel: fmt.Sprintf("linked stored state [%s]", observation), Value: view.Subject + " · " + view.Slot + ": " + view.Current}}
	if excerpt := stringFromMap(evidence, "evidence_excerpt"); excerpt != "" {
		parts = append(parts, prepareTurnMemoryPart{Key: prefix + "/evidence", Label: "state evidence", Value: excerpt})
	}
	for _, field := range []string{"observed_at", "occurrence_time", "effective_time", "validity"} {
		if value, exists := view.Payload[field]; exists {
			evidence[field] = value
		}
	}
	for _, field := range []string{"source_revision", "direct_evidence_ids", "observed_at", "occurrence_time", "effective_time", "validity", "repair_source_revision", "repair_recorded_turn"} {
		if value := evidence[field]; value != nil {
			parts = append(parts, prepareTurnMemoryPart{Key: prefix + "/" + field, Label: "state " + field, Value: prepareTurnPriorityScalarText(value)})
		}
	}
	if temporal := prepareTurnSourceTemporalContext(evidence, view.Payload); len(temporal) > 0 {
		parts = append(parts, prepareTurnMemoryPart{Key: prefix + "/time", Label: "state time (read only)", Value: mustCompactJSON(buildStoryTimeReading(temporal, clock))})
	}
	state.parts, state.partsBuilt = parts, true
	return parts
}

// prepareTurnTemporalParts keeps the temporal part built for each temporal
// context, for one clock. The part depends only on the context's contents
// (keyed with their Go types) and the clock.
type prepareTurnTemporalParts struct {
	mu    sync.Mutex
	parts map[string]prepareTurnMemoryPart
}

// prepareTurnWriteTypedKey writes a key that two values share only when they
// hold the same Go types and values: maps by sorted key, slices in order,
// other values with their type and %#v form.
func prepareTurnWriteTypedKey(b *strings.Builder, value any) {
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		b.WriteString("map{")
		for _, key := range keys {
			b.WriteString(strconv.Quote(key))
			b.WriteByte(':')
			prepareTurnWriteTypedKey(b, v[key])
			b.WriteByte(',')
		}
		b.WriteByte('}')
	case []any:
		b.WriteString("list[")
		for _, item := range v {
			prepareTurnWriteTypedKey(b, item)
			b.WriteByte(',')
		}
		b.WriteByte(']')
	default:
		fmt.Fprintf(b, "%T:%#v", value, value)
	}
}

// prepareTurnRowCost is the rune fold of a grouped row's rendered text: runes
// so far and the trailing whitespace runes among them. valid is false when it
// was not computed or some piece is not valid UTF-8.
type prepareTurnRowCost struct {
	total, trailing int
	valid           bool
}

func (c *prepareTurnRowCost) add(m prepareTurnTextRunes) bool {
	if !m.valid {
		c.valid = false
		return false
	}
	c.total += m.runes
	if m.trailingSpaces == m.runes {
		c.trailing += m.runes
	} else {
		c.trailing = m.trailingSpaces
	}
	return true
}

// Rows with more parts than this find repeated parts through an index.
const prepareTurnRowPartScanLimit = 16

// prepareTurnPartsHave reports whether parts has one with this key and text,
// scanning a few parts and otherwise building *index once.
func prepareTurnPartsHave(parts []prepareTurnMemoryFormPart, index *map[string][]string, part prepareTurnMemoryFormPart) bool {
	if len(parts) <= prepareTurnRowPartScanLimit {
		for _, existing := range parts {
			if existing.Key == part.Key && existing.Text == part.Text {
				return true
			}
		}
		return false
	}
	if *index == nil {
		*index = make(map[string][]string, len(parts))
		for _, existing := range parts {
			(*index)[existing.Key] = append((*index)[existing.Key], existing.Text)
		}
	}
	for _, text := range (*index)[part.Key] {
		if text == part.Text {
			return true
		}
	}
	return false
}

type prepareTurnTextStorage struct {
	data *byte
	n    int
}

// Runes of a valid UTF-8 text, and how many of its last runes are spaces
// (all of them when the text is only spaces).
type prepareTurnTextRunes struct {
	runes, trailingSpaces int
	valid                 bool
}

func measurePrepareTurnText(text string) prepareTurnTextRunes {
	if !utf8.ValidString(text) {
		return prepareTurnTextRunes{}
	}
	m := prepareTurnTextRunes{runes: utf8.RuneCountInString(text), valid: true}
	for end := len(text); end > 0; {
		r, size := utf8.DecodeLastRuneInString(text[:end])
		if !unicode.IsSpace(r) {
			break
		}
		m.trailingSpaces++
		end -= size
	}
	return m
}

func (l *prepareTurnMemoryReadingLayout) measure(text string) prepareTurnTextRunes {
	key := prepareTurnTextStorage{unsafe.StringData(text), len(text)}
	if m, ok := l.textRunes[key]; ok {
		return m
	}
	m := measurePrepareTurnText(text)
	if l.textRunes == nil {
		l.textRunes = map[prepareTurnTextStorage]prepareTurnTextRunes{}
	}
	l.textRunes[key] = m
	return m
}

// renderedCost is cost(renderPrepareTurnMemoryRow(row)) for a grouped row,
// computed from rune counts without building the text: the text starts with
// "- ", so strings.TrimSpace removes only its trailing spaces. ok is false
// when some piece is not valid UTF-8, where joined bytes could decode
// differently; the caller then renders the row.
func (l *prepareTurnMemoryReadingLayout) renderedCost(row prepareTurnMemoryReadingRow) (int, bool) {
	cost := l.foldRowCost(row, prepareTurnRowCost{}, 0)
	return cost.renderedCost(len(row.Parts))
}

func (c prepareTurnRowCost) renderedCost(parts int) (int, bool) {
	if parts == 0 {
		return 0, true
	}
	if !c.valid {
		return 0, false
	}
	return 1 + c.total - c.trailing, true
}

var (
	prepareTurnRowDash    = measurePrepareTurnText("- ")
	prepareTurnRowNewline = measurePrepareTurnText("\n")
	prepareTurnRowIndent  = measurePrepareTurnText("  ")
	prepareTurnRowOpen    = measurePrepareTurnText("[")
	prepareTurnRowClose   = measurePrepareTurnText("] ")
)

// foldRowCost folds the row's rendered pieces from part index from on, onto
// from's fold of the pieces before it; from 0 starts with the header.
func (l *prepareTurnMemoryReadingLayout) foldRowCost(row prepareTurnMemoryReadingRow, cost prepareTurnRowCost, from int) prepareTurnRowCost {
	if from == 0 {
		cost = prepareTurnRowCost{valid: true}
		if !cost.add(prepareTurnRowDash) || !cost.add(measurePrepareTurnText(row.Header)) || !cost.add(prepareTurnRowNewline) {
			return cost
		}
	}
	for i := from; i < len(row.Parts); i++ {
		part := row.Parts[i]
		if i > 0 {
			cost.add(prepareTurnRowNewline)
		}
		cost.add(prepareTurnRowIndent)
		if ref := row.partRef(part); ref != "" {
			if !cost.add(prepareTurnRowOpen) || !cost.add(measurePrepareTurnText(ref)) || !cost.add(prepareTurnRowClose) {
				return cost
			}
		}
		if !cost.add(l.measure(part.Text)) {
			return cost
		}
	}
	return cost
}

// partRef is row.partRefs[{part.Key, part.Text}]. A row holds few refs, so a
// direct comparison avoids hashing the part's (long) text for every part.
func (row prepareTurnMemoryReadingRow) partRef(part prepareTurnMemoryFormPart) string {
	if len(row.partRefs) > 8 {
		return row.partRefs[prepareTurnMemoryPartIdentity{part.Key, part.Text}]
	}
	for key, ref := range row.partRefs {
		if key.Key == part.Key && key.Text == part.Text {
			return ref
		}
	}
	return ""
}

// renderPrepareTurnMemoryRow is the delivered text of a grouped row.
func renderPrepareTurnMemoryRow(row prepareTurnMemoryReadingRow) string {
	lines := []string{}
	for _, part := range row.Parts {
		text := part.Text
		if ref := row.partRef(part); ref != "" {
			text = "[" + ref + "] " + text
		}
		lines = append(lines, "  "+text)
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace("- " + row.Header + "\n" + strings.Join(lines, "\n"))
}

type prepareTurnCurrentPartLookup struct {
	known   map[[2]prepareTurnTextStorage]prepareTurnCurrentPartLookupEntry
	version int
}

type prepareTurnCurrentPartLookupEntry struct {
	found   bool
	version int
}

func newPrepareTurnCurrentPartLookup() *prepareTurnCurrentPartLookup {
	return &prepareTurnCurrentPartLookup{known: map[[2]prepareTurnTextStorage]prepareTurnCurrentPartLookupEntry{}}
}

func (l *prepareTurnMemoryReadingLayout) isCurrentPart(shared prepareTurnMemoryPartIdentity) bool {
	lookup := l.currentLookup
	if lookup == nil {
		return l.currentParts[shared]
	}
	key := [2]prepareTurnTextStorage{prepareTurnStorageOf(shared.Key), prepareTurnStorageOf(shared.Text)}
	if entry, ok := lookup.known[key]; ok && (entry.found || entry.version == lookup.version) {
		return entry.found
	}
	found := l.currentParts[shared]
	lookup.known[key] = prepareTurnCurrentPartLookupEntry{found, lookup.version}
	return found
}

// prepareTurnMemoryCandidateRowWithPlain takes the row's plain line, "- "
// followed by the rendered text, when the caller already has it.
func prepareTurnMemoryCandidateRowWithPlain(c prepareTurnPriorityMemoryCandidate, order int, ref, plain string) prepareTurnMemoryReadingRow {
	var parts []prepareTurnMemoryFormPart
	if c.Minimum != nil {
		parts = prepareTurnMemoryDeliveryParts(c.Minimum.Parts)
	}
	return prepareTurnMemoryCandidateRowWithParts(c, order, ref, plain, parts)
}

// prepareTurnMemoryCandidateRowWithParts takes the delivery parts of
// c.Minimum, which the row only reads.
// prepareTurnMemoryPartsDistinct reports whether no two parts have the same
// key and text.
func prepareTurnMemoryPartsDistinct(parts []prepareTurnMemoryFormPart) bool {
	var index map[string][]string
	for i, part := range parts {
		if prepareTurnPartsHave(parts[:i], &index, part) {
			return false
		}
		if index != nil {
			index[part.Key] = append(index[part.Key], part.Text)
		}
	}
	return true
}

// prepareTurnStateMentionIndex finds the source tokens that name a subject the
// way the per-token check did: strings.EqualFold, or a non-ASCII inflection
// one rune longer or shorter (prepareTurnPriorityInflectedNonASCIIMatch).
type prepareTurnStateMentionIndex struct {
	tokens       []string
	factsByToken [][]int          // ascending fact indexes per token id
	byFold       map[string][]int // case-fold key -> token ids
	byTrimmed    map[string][]int // trimmed token -> token ids
	byDropLast   map[string][]int // trimmed token minus its last rune -> token ids
}

func newPrepareTurnStateMentionIndex(sourceTerms [][]string) *prepareTurnStateMentionIndex {
	index := &prepareTurnStateMentionIndex{byFold: map[string][]int{}, byTrimmed: map[string][]int{}, byDropLast: map[string][]int{}}
	ids := map[string]int{}
	for fact, terms := range sourceTerms {
		for _, token := range terms {
			id, ok := ids[token]
			if !ok {
				id = len(index.tokens)
				ids[token] = id
				index.tokens = append(index.tokens, token)
				index.factsByToken = append(index.factsByToken, nil)
				index.byFold[prepareTurnCaseFoldKey(token)] = append(index.byFold[prepareTurnCaseFoldKey(token)], id)
				trimmed := prepareTurnRuneKey(strings.TrimSpace(token))
				index.byTrimmed[trimmed] = append(index.byTrimmed[trimmed], id)
				if dropped, ok := prepareTurnDropLastRune(trimmed); ok {
					index.byDropLast[dropped] = append(index.byDropLast[dropped], id)
				}
			}
			if postings := index.factsByToken[id]; len(postings) == 0 || postings[len(postings)-1] != fact {
				index.factsByToken[id] = append(postings, fact)
			}
		}
	}
	return index
}

// matchingTokens returns the ids of tokens that name subject. Candidates come
// from the indexes; each is confirmed with the original predicate.
func (index *prepareTurnStateMentionIndex) matchingTokens(subject string) []int {
	return index.matchingTokensFor(subject, newPrepareTurnSubjectKeys(subject))
}

// prepareTurnSubjectKeys are the index keys matchingTokens derives from a
// subject; a subject matched against many indexes derives them once.
type prepareTurnSubjectKeys struct {
	fold, trimmed, dropped string
	hasDropped             bool
}

func newPrepareTurnSubjectKeys(subject string) prepareTurnSubjectKeys {
	keys := prepareTurnSubjectKeys{fold: prepareTurnCaseFoldKey(subject), trimmed: prepareTurnRuneKey(strings.TrimSpace(subject))}
	keys.dropped, keys.hasDropped = prepareTurnDropLastRune(keys.trimmed)
	return keys
}

func (index *prepareTurnStateMentionIndex) matchingTokensFor(subject string, keys prepareTurnSubjectKeys) []int {
	candidates := append([]int(nil), index.byFold[keys.fold]...)
	candidates = append(candidates, index.byDropLast[keys.trimmed]...)
	if keys.hasDropped {
		candidates = append(candidates, index.byTrimmed[keys.dropped]...)
	}
	if len(candidates) == 0 {
		return nil
	}
	var matched []int
	seen := map[int]bool{}
	for _, id := range candidates {
		if seen[id] {
			continue
		}
		seen[id] = true
		token := index.tokens[id]
		if strings.EqualFold(token, subject) || prepareTurnPriorityInflectedNonASCIIMatch(token, subject) {
			matched = append(matched, id)
		}
	}
	return matched
}

// prepareTurnCaseFoldKey maps each rune to the smallest rune of its simple
// case-folding orbit, so strings.EqualFold(a, b) holds exactly when the keys
// are equal (invalid UTF-8 reads as U+FFFD in both).
func prepareTurnCaseFoldKey(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		smallest := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < smallest {
				smallest = f
			}
		}
		b.WriteRune(smallest)
	}
	return b.String()
}

// prepareTurnRuneKey is string([]rune(s)): the inflection check compares
// rune slices, which read each invalid UTF-8 byte as U+FFFD.
func prepareTurnRuneKey(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return string([]rune(s))
}

func prepareTurnDropLastRune(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	_, size := utf8.DecodeLastRuneInString(s)
	return s[:len(s)-size], true
}
