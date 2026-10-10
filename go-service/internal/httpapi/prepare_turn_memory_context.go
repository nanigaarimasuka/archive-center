package httpapi

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"github.com/risulongmemory/archive-center-go/internal/store"
)

// Immutable, request-owned reading material. Facts, their IDs and stored rows
// remain independent. A minimum form is built before either Go or AI selection.
type prepareTurnMemoryPart struct {
	Key, Label, Value string
	DeliveryLabel     string `json:",omitempty"`
	// Other slots matched by name remain scoring/preprocessing context for an
	// explicitly typed assertion. Untyped facts keep their established reading.
	ReferenceOnly bool `json:",omitempty"`
	FactTexts     []string
}
type prepareTurnMemoryContext struct {
	Path, Label string
	DisplayPath string `json:",omitempty"`
	Parts       []prepareTurnMemoryPart
	fingerprint [32]byte
}

// Source preparation is serialized by the existing request owner. Cache only
// this immutable value's fingerprint, never any question or selection result.
func (c *prepareTurnMemoryContext) sourceFingerprint() [32]byte {
	return c.fingerprintWith(prepareTurnMemoryPartDigest)
}

func (c *prepareTurnMemoryContext) fingerprintWith(partDigest func(prepareTurnMemoryPart) [32]byte) [32]byte {
	if c == nil {
		return [32]byte{}
	}
	if c.fingerprint == ([32]byte{}) {
		c.fingerprint = c.encodedFingerprint(partDigest)
	}
	return c.fingerprint
}

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

type prepareTurnMemoryFormPart struct {
	Key, Text string
	SharedKey string
	Refs      []string
	// Nil uses the reading text; an empty override omits only typed bookkeeping.
	DeliveryText *string
}
type prepareTurnMemoryForm struct {
	Group, Heading, Text, Meaning string
	Parts                         []prepareTurnMemoryFormPart
	Refs                          []string
	Chars                         int
	// kept is set on forms kept across requests: what later requests derive
	// from them is then derived once.
	kept *prepareTurnKeptForm
}

// prepareTurnKeptForm holds what is derived from a kept form: its delivery
// parts, prepareTurnMemoryDeliveryParts(parts), and whether those are
// distinct, for the parts it was made for (a copy of a form shares it; a
// copy whose parts changed no longer matches them); and its meaning's
// lexical analysis.
type prepareTurnKeptForm struct {
	once     sync.Once
	source   []prepareTurnMemoryFormPart
	parts    []prepareTurnMemoryFormPart
	distinct bool
	lexical  prepareTurnKeptLexical
}

// keptLexical is nil-safe; it is nil for forms that are not kept.
func (f *prepareTurnMemoryForm) keptLexical() *prepareTurnKeptLexical {
	if f == nil || f.kept == nil {
		return nil
	}
	return &f.kept.lexical
}

// keptDelivery returns the form's delivery parts and whether they are
// distinct when the form keeps them; ok is false otherwise.
func (f *prepareTurnMemoryForm) keptDelivery() (parts []prepareTurnMemoryFormPart, distinct, ok bool) {
	d := f.kept
	if d == nil || len(d.source) != len(f.Parts) || (len(f.Parts) > 0 && &d.source[0] != &f.Parts[0]) {
		return nil, false, false
	}
	d.once.Do(func() {
		d.parts = prepareTurnMemoryDeliveryParts(d.source)
		d.distinct = prepareTurnMemoryPartsDistinct(d.parts)
	})
	return d.parts, d.distinct, true
}

func prepareTurnMemoryPath(path []string) string {
	parts := make([]string, len(path))
	for i, part := range path {
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(part, "~", "~0"), "/", "~1")
	}
	return "/" + strings.Join(parts, "/")
}

// These labels retain explicit structural roles; they never infer a character,
// event or relationship from model prose. Unknown fields keep their own value.
func prepareTurnMemoryAnchor(field string) bool {
	switch strings.ToLower(field) {
	case "name", "character_name", "display_name", "canonical_name", "subject", "subject_entity", "actor", "actor_name", "item", "scope", "scope_name":
		return true
	}
	return false
}
func prepareTurnMemoryCondition(field string) bool {
	switch strings.ToLower(field) {
	case "condition", "conditions", "exception", "exceptions", "restriction", "restrictions", "when", "unless":
		return true
	}
	return false
}

func prepareTurnAttachStructuredContexts(prefix string, value any, path []string, facts []prepareTurnPriorityMemoryFact) {
	byPath := make(map[string]int, len(facts))
	for i := range facts {
		byPath[facts[i].SourcePath] = i
	}
	var visit func(any, []string, []prepareTurnMemoryPart)
	visit = func(value any, path []string, inherited []prepareTurnMemoryPart) {
		switch node := value.(type) {
		case []any:
			for i, child := range node {
				visit(child, append(append([]string{}, path...), strconv.Itoa(i)), inherited)
			}
		case map[string]any:
			keys := make([]string, 0, len(node))
			for key := range node {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			parts := map[string]prepareTurnMemoryPart{}
			qualifiers := []prepareTurnMemoryPart{}
			anchors := append([]prepareTurnMemoryPart(nil), inherited...)
			for _, key := range keys {
				p := prepareTurnMemoryPath(append(append([]string{}, path...), key))
				if i, ok := byPath[p]; ok {
					part := prepareTurnMemoryPart{Key: p, Label: key, Value: prepareTurnPriorityScalarText(node[key]), FactTexts: []string{facts[i].Text}}
					parts[key] = part
					if prepareTurnMemoryAnchor(key) {
						anchors = append(anchors, part)
					}
					if prepareTurnMemoryCondition(key) {
						qualifiers = append(qualifiers, part)
					}
				} else if prepareTurnMemoryCondition(key) {
					// An explicit condition can itself be an array or object. Keep
					// that value intact rather than dropping its nested qualifiers.
					part := prepareTurnMemoryPart{Key: p, Label: key, Value: mustCompactJSON(node[key])}
					for _, fact := range facts {
						if strings.HasPrefix(fact.SourcePath, p+"/") {
							part.FactTexts = append(part.FactTexts, fact.Text)
						}
					}
					qualifiers = append(qualifiers, part)
				}
			}
			for _, key := range keys {
				part, scalar := parts[key]
				if !scalar {
					if proposition, ok := parts["rule"]; ok && (prepareTurnMemoryCondition(key) || key == "evidence_excerpt") {
						// A nested qualifier is still part of this rule, not a new
						// independent record. Keep its complete explicit value.
						minimum := append([]prepareTurnMemoryPart(nil), anchors...)
						minimum = append(minimum, proposition)
						minimum = append(minimum, qualifiers...)
						p := prepareTurnMemoryPath(append(append([]string{}, path...), key))
						if key == "evidence_excerpt" {
							minimum = append(minimum, prepareTurnMemoryPart{Key: p, Label: key, Value: mustCompactJSON(node[key])})
						}
						reading := &prepareTurnMemoryContext{Path: prepareTurnMemoryPath(path), Label: prefix, Parts: minimum}
						for i := range facts {
							if strings.HasPrefix(facts[i].SourcePath, p+"/") {
								facts[i].Reading = reading
							}
						}
						continue
					}
					visit(node[key], append(append([]string{}, path...), key), anchors)
					continue
				}
				minimum := append([]prepareTurnMemoryPart(nil), anchors...)
				seen := map[string]bool{}
				for _, p := range minimum {
					seen[p.Key] = true
				}
				add := func(p prepareTurnMemoryPart) {
					if p.Key != "" && !seen[p.Key] {
						minimum = append(minimum, p)
						seen[p.Key] = true
					}
				}
				add(part)
				// A rule's scope, exception and quotation qualify this proposition;
				// none is a free-standing rule when selected on its own.
				if proposition, ok := parts["rule"]; ok {
					add(proposition)
					for _, qualifier := range qualifiers {
						add(qualifier)
					}
				}
				// A transfer's direction is explicit in its two named fields.
				if key == "giver" || key == "recipient" {
					add(parts["giver"])
					add(parts["recipient"])
				}
				if !prepareTurnMemoryAnchor(key) && key != "evidence_excerpt" && key != "description" {
					for _, qualifier := range qualifiers {
						add(qualifier)
					}
				}
				// Even a lone JSON field has an observed path/value. Retaining
				// those directly avoids repeating its display prefix inside a
				// source header when typed row scope is attached later.
				facts[byPath[part.Key]].Reading = &prepareTurnMemoryContext{Path: prepareTurnMemoryPath(path), Label: prefix, Parts: minimum}
			}
		}
	}
	visit(value, path, nil)
}

type prepareTurnMemoryReadingSource struct {
	Ref, Occurrence, Parent, Lane, Visibility, Owner, Viewers string
	Turn                                                      int
}

func prepareTurnMemorySourceKey(c prepareTurnPriorityMemoryCandidate) prepareTurnMemoryReadingSource {
	// Same source in another lane/holder remains a separate reading occurrence.
	return prepareTurnMemoryReadingSource{c.SourceRef, c.SourceOccurrence, c.ParentLineText, c.Lane, c.Visibility, c.PerspectiveOwner, strings.Join(c.AllowedViewers, "\x1f"), c.SourceTurn}
}

type prepareTurnMemoryGroupKey struct {
	Source prepareTurnMemoryReadingSource
	Path   string
}
type prepareTurnMemoryFormKey struct {
	Group      string
	Content    [32]byte
	References string
}

// primeScores, when given, fills relevance's memo for the reading meanings
// ahead of the serial scoring loop.
func prepareTurnBuildReadingForms(candidates []prepareTurnPriorityMemoryCandidate, relevance func(string) float64, preparation *prepareTurnRequestPreparation, primeScores ...func([]string)) {
	defer preparation.measurement().start("assembly.reading_forms").end()
	refs := map[prepareTurnMemoryReadingSource]map[string]string{}
	for _, c := range candidates {
		if c.Reading != nil {
			refs[prepareTurnMemorySourceKey(c)] = nil
		}
	}
	if len(refs) == 0 {
		return
	}
	groups := map[prepareTurnMemoryGroupKey]string{}
	forms := map[prepareTurnMemoryFormKey]*prepareTurnMemoryForm{}
	if preparation != nil {
		groups, forms = preparation.readingGroups, preparation.readingForms
	}
	for _, c := range candidates {
		key := prepareTurnMemorySourceKey(c)
		if _, needed := refs[key]; !needed {
			continue
		}
		if refs[key] == nil {
			refs[key] = map[string]string{}
		}
		refs[key][c.CompleteText] = c.CanonicalFactID
	}
	// Keys are resolved in candidate order. A form missing from the cache is
	// built once, from its first candidate; builds are independent of each
	// other and run concurrently. Cache writes and scoring keep candidate order.
	type newForm struct {
		key    prepareTurnMemoryFormKey
		source int
		form   *prepareTurnMemoryForm
		values []string
	}
	var created []newForm
	createdIndex := map[prepareTurnMemoryFormKey]int{}
	formOf := make([]int, len(candidates)) // -1: no reading, >= 0: created index
	cachedForm := make([]*prepareTurnMemoryForm, len(candidates))
	for i := range candidates {
		formOf[i] = -1
		c := &candidates[i]
		if c.Reading == nil {
			continue
		}
		key := prepareTurnMemorySourceKey(*c)
		group := prepareTurnMemoryGroupKey{key, c.Reading.Path}
		groupID := groups[group]
		if groupID == "" {
			b, _ := json.Marshal(group)
			groupID = fmt.Sprintf("%x", sha256.Sum256(b))
			groups[group] = groupID
		}
		// Contents and provenance are immutable, but a discovery may add a
		// previously missing reference. Include the ordered bindings in the key.
		var bindings strings.Builder
		for _, part := range c.Reading.Parts {
			for _, text := range part.FactTexts {
				bindings.WriteString(refs[key][text])
				bindings.WriteByte('\x1f')
			}
		}
		formKey := prepareTurnMemoryFormKey{groupID, preparation.readingFingerprint(c.Reading), bindings.String()}
		if form := forms[formKey]; form != nil {
			cachedForm[i] = form
			continue
		}
		index, ok := createdIndex[formKey]
		if !ok {
			index = len(created)
			createdIndex[formKey] = index
			created = append(created, newForm{key: formKey, source: i, form: preparation.keptReadingForms().get(formKey)})
		}
		formOf[i] = index
	}
	// Shared state parts recur across thousands of readings. Their reading and
	// delivery texts depend only on the part, so each worker renders each once.
	type partTextKey struct{ label, value string }
	// The delivery text a part gets, if any. It depends only on the part,
	// so equal parts share one string (and its storage) across readings.
	type partDisplay struct {
		delivery    string
		hasDelivery bool
	}
	type partDisplayKey struct {
		key, label, value, deliveryLabel string
		referenceOnly                    bool
	}
	// A part's value is long; a part seen before with the same value storage
	// is found without hashing the value.
	type partStoredKey struct {
		key, label, deliveryLabel string
		referenceOnly             bool
		value                     prepareTurnTextStorage
	}
	type partResolved struct {
		text  string
		shown partDisplay
	}
	// A source's strings are mostly shared between its candidates; the shared
	// prefix is found by their storage before hashing their contents.
	type sourceStorageKey struct {
		ref, occurrence, parent, visibility, owner, viewers prepareTurnTextStorage
		turn                                                int
	}
	type partMemo struct {
		texts          map[partTextKey]string
		displays       map[partDisplayKey]partDisplay
		stored         map[partStoredKey]partResolved
		prefixes       map[prepareTurnMemoryReadingSource]string
		storedPrefixes map[sourceStorageKey]string
		runes          map[prepareTurnTextStorage]prepareTurnTextRunes
	}
	memos := sync.Pool{New: func() any {
		return &partMemo{texts: map[partTextKey]string{}, displays: map[partDisplayKey]partDisplay{}, stored: map[partStoredKey]partResolved{},
			prefixes: map[prepareTurnMemoryReadingSource]string{}, storedPrefixes: map[sourceStorageKey]string{}, runes: map[prepareTurnTextStorage]prepareTurnTextRunes{}}
	}}
	storageOf := func(text string) prepareTurnTextStorage {
		return prepareTurnTextStorage{unsafe.StringData(text), len(text)}
	}
	built := make([]bool, len(created))
	prepareTurnParallelFor(len(created), func(index int) {
		if created[index].form != nil {
			// Kept from an earlier request; only its part values are needed.
			parts := candidates[created[index].source].Reading.Parts
			values := make([]string, len(parts))
			for i, p := range parts {
				values[i] = p.Value
			}
			created[index].values = values
			return
		}
		built[index] = true
		memo := memos.Get().(*partMemo)
		defer memos.Put(memo)
		c := &candidates[created[index].source]
		key := prepareTurnMemorySourceKey(*c)
		form := &prepareTurnMemoryForm{Group: created[index].key.Group}
		heading := strings.TrimSpace(c.Reading.Label)
		path := c.Reading.Path
		if c.Reading.DisplayPath != "" {
			path = c.Reading.DisplayPath
		}
		if path != "" && path != "/" {
			heading += " [" + path + "]"
		}
		form.Heading = strings.TrimSpace(heading)
		values := make([]string, 0, len(c.Reading.Parts))
		sourceRefs := refs[key]
		form.Parts = make([]prepareTurnMemoryFormPart, 0, len(c.Reading.Parts))
		seenRefs := map[string]bool{}
		sharedSource := key
		sharedSource.Lane = ""
		sourceStorage := sourceStorageKey{storageOf(key.Ref), storageOf(key.Occurrence), storageOf(key.Parent), storageOf(key.Visibility), storageOf(key.Owner), storageOf(key.Viewers), key.Turn}
		sharedPrefix, ok := memo.storedPrefixes[sourceStorage]
		if !ok {
			sharedPrefix, ok = memo.prefixes[sharedSource]
			if !ok {
				sharedBytes, _ := json.Marshal(sharedSource)
				sharedPrefix = fmt.Sprintf("@source/%x/", sha256.Sum256(sharedBytes))
				memo.prefixes[sharedSource] = sharedPrefix
			}
			memo.storedPrefixes[sourceStorage] = sharedPrefix
		}
		for _, p := range c.Reading.Parts {
			// Preprocessing reads and budgets the original candidate form.
			// Final-prompt cleanup must not expand its candidate packet.
			values = append(values, p.Value)
			if p.Label == "source_session_id" {
				continue
			}
			storedKey := partStoredKey{p.Key, p.Label, p.DeliveryLabel, p.ReferenceOnly, prepareTurnTextStorage{unsafe.StringData(p.Value), len(p.Value)}}
			resolved, stored := memo.stored[storedKey]
			text, ok := resolved.text, stored
			if !ok {
				text, ok = memo.texts[partTextKey{p.Label, p.Value}]
			}
			if !ok {
				text = p.Value
				switch p.Label {
				case "source-relative time (last confirmed clock; read only)", "state time (read only)":
					text = storyTimePromptReading(parseJSONMap(p.Value))
				case "schedule reading (read only)":
					text = storyTimePromptSchedule(parseJSONMap(p.Value))
				default:
					if p.Label != "" {
						text = p.Label + ": " + text
					}
				}
				memo.texts[partTextKey{p.Label, p.Value}] = text
			}
			part := prepareTurnMemoryFormPart{Key: p.Key, Text: text, SharedKey: sharedPrefix + p.Key}
			shown, ok := resolved.shown, stored
			displayKey := partDisplayKey{p.Key, p.Label, p.Value, p.DeliveryLabel, p.ReferenceOnly}
			if !ok {
				shown, ok = memo.displays[displayKey]
			}
			if !ok {
				delivery, display := prepareTurnMemoryPartDisplay(p)
				if !display {
					shown = partDisplay{delivery: delivery, hasDelivery: true}
				} else if delivery != p.Value || p.DeliveryLabel != "" {
					label := p.Label
					if p.DeliveryLabel != "" {
						label = p.DeliveryLabel
					}
					if label != "" {
						delivery = label + ": " + delivery
					}
					shown = partDisplay{delivery: delivery, hasDelivery: true}
				}
				memo.displays[displayKey] = shown
			}
			if !stored {
				memo.stored[storedKey] = partResolved{text, shown}
			}
			if shown.hasDelivery {
				delivery := shown.delivery
				part.DeliveryText = &delivery
			}
			for _, factText := range p.FactTexts {
				if ref := sourceRefs[factText]; ref != "" {
					part.Refs = append(part.Refs, ref)
					if !seenRefs[ref] {
						seenRefs[ref] = true
						form.Refs = append(form.Refs, ref)
					}
				}
			}
			form.Parts = append(form.Parts, part)
		}
		// heading + "\n" + "  "-indented part texts joined by "\n"
		size := len(form.Heading) + 1
		for i, part := range form.Parts {
			if i > 0 {
				size++
			}
			size += 2 + len(part.Text)
		}
		var formText strings.Builder
		formText.Grow(size)
		formText.WriteString(form.Heading)
		formText.WriteByte('\n')
		for i, part := range form.Parts {
			if i > 0 {
				formText.WriteByte('\n')
			}
			formText.WriteString("  ")
			formText.WriteString(part.Text)
		}
		if len(form.Parts) == 0 {
			form.Parts = nil // as when parts were only appended
		}
		form.Meaning = strings.Join(values, "\n")
		form.Text = strings.TrimSpace(formText.String())
		form.Chars = prepareTurnMemoryFormChars(form.Heading, form.Parts, form.Text, func(text string) prepareTurnTextRunes {
			m, ok := memo.runes[storageOf(text)]
			if !ok {
				m = measurePrepareTurnText(text)
				memo.runes[storageOf(text)] = m
			}
			return m
		})
		created[index].form, created[index].values = form, values
	})
	meanings := make([]prepareTurnJoinedLexicalText, len(created))
	for index, item := range created {
		forms[item.key] = item.form
		meanings[index] = prepareTurnJoinedLexicalText{joined: item.form.Meaning, parts: item.values}
	}
	if kept := preparation.keptReadingForms(); kept != nil {
		keys, newForms := []prepareTurnMemoryFormKey{}, []*prepareTurnMemoryForm{}
		for index, item := range created {
			if built[index] {
				keys, newForms = append(keys, item.key), append(newForms, item.form)
			}
		}
		kept.put(keys, newForms)
		for index, item := range created {
			meanings[index].kept = item.form.keptLexical()
		}
	}
	preparation.primeJoinedLexicalTexts(meanings)
	if len(primeScores) > 0 {
		texts := make([]string, 0, len(candidates))
		for i := range candidates {
			if formOf[i] >= 0 {
				texts = append(texts, created[formOf[i]].form.Meaning)
			} else if cachedForm[i] != nil {
				texts = append(texts, cachedForm[i].Meaning)
			}
		}
		primeScores[0](texts)
	}
	for i := range candidates {
		c := &candidates[i]
		form := cachedForm[i]
		if formOf[i] >= 0 {
			form = created[formOf[i]].form
		}
		if form == nil {
			continue
		}
		c.Minimum = form
		c.ContextRelevance = relevance(form.Meaning)
		c.Relevance = math.Max(c.OriginalRelevance, c.ContextRelevance)
		c.FinalScore = prepareTurnPriorityScore(c.Relevance, c.Importance, c.Recency, c.ContinuityBonus, c.StructuredBias)
	}
	groupScores := map[string]float64{}
	for _, c := range candidates {
		if c.Minimum != nil {
			groupScores[c.Minimum.Group] = math.Max(groupScores[c.Minimum.Group], c.FinalScore)
		}
	}
	for i := range candidates {
		if candidates[i].Minimum != nil {
			candidates[i].ContextGroupScore = groupScores[candidates[i].Minimum.Group]
		}
	}
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

// Render only typed backend additions here. Original facts/quotations and the
// source reading (including IDs used by selection and diagnostics) stay intact.
func prepareTurnMemoryPartDisplay(p prepareTurnMemoryPart) (string, bool) {
	if p.ReferenceOnly {
		return "", false
	}
	if !strings.HasPrefix(p.Key, "@lifecycle/") && !strings.HasPrefix(p.Key, "@current/") && !strings.HasPrefix(p.Key, "@field_current/") && !strings.HasPrefix(p.Key, "@field/") {
		return p.Value, true
	}
	field := p.Label
	field = strings.TrimPrefix(strings.TrimPrefix(field, "state "), "current ")
	field = strings.TrimPrefix(field, "restoration audit ")
	switch field {
	case "source_revision", "direct_evidence_ids", "evidence_refs", "repair_source_revision", "progression source":
		return "", false
	case "observed_at":
		if value := parseJSONMap(p.Value); len(value) > 0 {
			return storyTimePromptCoordinate(value), true
		}
	case "progression details", "occurrence_time", "effective_time", "validity", "learned_time":
		if value := parseJSONMap(p.Value); len(value) > 0 {
			return prepareTurnMemoryDisplayFields(value), true
		}
	}
	return p.Value, true
}

// Keep every supplied story field, including unfamiliar nested conditions.
// Strings are never parsed or rewritten, even when they contain literal JSON.
func prepareTurnMemoryDisplayFields(value any) string {
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			text := prepareTurnMemoryDisplayFields(v[key])
			if _, nested := v[key].(map[string]any); nested {
				text = "(" + text + ")"
			}
			parts = append(parts, key+": "+text)
		}
		return strings.Join(parts, "; ")
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			parts = append(parts, prepareTurnMemoryDisplayFields(item))
		}
		return "[" + strings.Join(parts, " | ") + "]"
	case []string:
		return "[" + strings.Join(v, " | ") + "]"
	case string:
		return v
	case nil:
		return "null"
	default:
		return prepareTurnPriorityScalarText(v)
	}
}

func prepareTurnMemoryReadingText(c prepareTurnPriorityMemoryCandidate) string {
	if c.Minimum != nil {
		return c.Minimum.Text
	}
	return c.CompleteText
}

// A manual restoration has its own recorded revision but retains the restored
// state's original observation and proof, including an explicitly unknown turn.
func prepareTurnCurrentStateReadingOrigin(current store.StatusCurrentValue) (store.StatusCurrentValue, map[string]any) {
	evidence := parseJSONMap(current.EvidenceJSON)
	if turn, restored := evidence["restored_source_turn"]; restored {
		current.SourceTurn = intFromAny(turn, 0)
		original := map[string]any{}
		for key, value := range mapFromAny(evidence["restored_evidence"]) {
			original[key] = value
		}
		original["repair_source_revision"] = evidence["source_revision"]
		original["repair_recorded_turn"] = evidence["repair_recorded_turn"]
		evidence = original
	}
	return current, evidence
}

// A recalled historical promise keeps its own identity and source time. Its
// current lifecycle is source-linked reading material, independent of whether
// the completion Memory also won an ordinary vector recall slot.
func prepareTurnLifecycleReadings(values []store.StatusCurrentValue, clocks ...map[string]any) map[string][]prepareTurnMemoryPart {
	out := map[string][]prepareTurnMemoryPart{}
	var clock map[string]any
	if len(clocks) > 0 {
		clock = clocks[0]
	}
	for _, view := range narrativeCurrentStateViews(values) {
		// Preserve the existing narrative current-state public projection boundary.
		switch view.Scope {
		case "belief", "rumor", "secret":
			continue
		}
		key := normalizeNarrativeLifecycleKey(stringFromMap(view.Payload, "lifecycle_key"))
		if key == "" {
			continue
		}
		prefix := fmt.Sprintf("@lifecycle/%s/%s/%s", key, view.Value.OwnerScope, view.Value.OwnerID)
		origin, evidence := prepareTurnCurrentStateReadingOrigin(view.Value)
		observation := "source turn unknown"
		if origin.SourceTurn > 0 {
			observation = fmt.Sprintf("source turn %d", origin.SourceTurn)
		}
		label := fmt.Sprintf("current progression [lifecycle %s; %s; status_current_values:%d]", key, observation, view.Value.ID)
		deliveryLabel := fmt.Sprintf("current progression [lifecycle %s; %s]", key, observation)
		text := view.Current
		if view.Subject != "" {
			text = view.Subject + ": " + text
		}
		if transition := stringFromMap(view.Payload, "transition"); transition != "" {
			text += " (" + transition + ")"
		}
		parts := []prepareTurnMemoryPart{{Key: prefix, Label: label, DeliveryLabel: deliveryLabel, Value: text}}
		if details := mapFromAny(view.Payload["lifecycle_details"]); len(details) > 0 {
			parts = append(parts, prepareTurnMemoryPart{Key: prefix + "/details", Label: "progression details", Value: prepareTurnPriorityScalarText(details)})
			scheduleSource := make(map[string]any, len(details)+1)
			for key, value := range details {
				scheduleSource[key] = value
			}
			scheduleSource["lifecycle_transition"] = stringFromMap(view.Payload, "transition")
			if schedule := buildCommitmentScheduleReading(scheduleSource, clock); len(schedule) > 0 {
				parts = append(parts, prepareTurnMemoryPart{Key: prefix + "/schedule", Label: "schedule reading (read only)", Value: mustCompactJSON(schedule)})
			}
		}
		if excerpt := stringFromMap(evidence, "evidence_excerpt"); excerpt != "" {
			parts = append(parts, prepareTurnMemoryPart{Key: prefix + "/evidence", Label: "current progression evidence", Value: excerpt})
		}
		refs := []string{}
		if source := stringFromMap(evidence, "source"); source != "" {
			refs = append(refs, source)
		}
		if revision := stringFromMap(evidence, "source_revision"); revision != "" {
			refs = append(refs, "source revision "+revision)
		}
		if ids := sliceFromAny(evidence["direct_evidence_ids"]); len(ids) > 0 {
			refs = append(refs, "direct evidence "+mustCompactJSON(ids))
		}
		if len(refs) > 0 {
			parts = append(parts, prepareTurnMemoryPart{Key: prefix + "/source", Label: "progression source", Value: strings.Join(refs, "; ")})
		}
		for _, name := range []string{"repair_source_revision", "repair_recorded_turn"} {
			if raw := evidence[name]; raw != nil {
				parts = append(parts, prepareTurnMemoryPart{Key: prefix + "/" + name, Label: "restoration audit " + name, Value: prepareTurnPriorityScalarText(raw)})
			}
		}
		out[key] = append(out[key], parts...)
	}
	return out
}

func prepareTurnAttachLifecycleContext(out *prepareTurnInjectionAssembly, values []store.StatusCurrentValue, clocks ...map[string]any) {
	prepareTurnAttachLifecycleReadings(out, prepareTurnLifecycleReadings(values, clocks...))
}

// prepareTurnAttachLifecycleReadings attaches readings built by
// prepareTurnLifecycleReadings, which it only reads.
func prepareTurnAttachLifecycleReadings(out *prepareTurnInjectionAssembly, readings map[string][]prepareTurnMemoryPart) {
	for i := range out.PriorityFactSeeds {
		fact := &out.PriorityFactSeeds[i].Fact
		parts := readings[normalizeNarrativeLifecycleKey(fact.LifecycleKey)]
		if len(parts) == 0 {
			continue
		}
		reading := prepareTurnMemoryContext{Path: fact.SourcePath, Parts: []prepareTurnMemoryPart{{Key: fact.SourcePath, Value: fact.Text, FactTexts: []string{fact.Text}}}}
		if fact.Reading != nil {
			reading = *fact.Reading
			reading.Parts = append([]prepareTurnMemoryPart(nil), fact.Reading.Parts...)
		}
		reading.fingerprint = [32]byte{}
		reading.Parts = append(reading.Parts, parts...)
		fact.Reading = &reading
	}
}

// Recalled descriptions keep the stored state of the named subject beside them,
// even when the input describes the subject indirectly. This is reading context:
// source identity, observation time and the canonical state owner stay unchanged.
func prepareTurnAttachCurrentStateContext(out *prepareTurnInjectionAssembly, values []store.StatusCurrentValue, clock map[string]any) {
	prepareTurnAttachCurrentStatePlan(out, newPrepareTurnCurrentStatePlan(values, out.PriorityEntityAliases, clock))
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

func prepareTurnAttachCurrentStatePlan(out *prepareTurnInjectionAssembly, plan *prepareTurnCurrentStatePlan) {
	// Compile only original source text once. Attached readings cannot recursively
	// introduce another subject, and large state registries do not re-tokenize it.
	sourceTerms := make([][]string, len(out.PriorityFactSeeds))
	sourcePhrases := make([]string, len(out.PriorityFactSeeds))
	wordBreak := func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '_' && r != '-' }
	for i, seed := range out.PriorityFactSeeds {
		text := seed.Fact.Text
		if seed.Fact.Reading != nil {
			for _, part := range seed.Fact.Reading.Parts {
				if !strings.HasPrefix(part.Key, "@") {
					text += "\n" + part.Value
				}
			}
		}
		sourceTerms[i] = prepareTurnRecallTerms(text)
		sourcePhrases[i] = " " + strings.Join(strings.FieldsFunc(strings.ToLower(text), wordBreak), " ") + " "
	}
	// Index the sources once: which facts contain each distinct token, and
	// which tokens could match a subject name. Each state then finds the
	// facts that mention it directly instead of scanning every fact's tokens.
	index := newPrepareTurnStateMentionIndex(sourceTerms)
	// Each matched fact gets one private reading copy in this pass, sized for
	// all the parts the states attach; they are collected first, in order.
	type stateAttachment struct {
		fact   int
		parts  []prepareTurnMemoryPart
		linked bool
	}
	var attachments []stateAttachment
	// Facts whose source phrase has a word, for multi-word subject names: a
	// phrase containing " a b " has the whole word a.
	var factsByWord map[string][]int
	// A fact's entity key does not depend on the state; group facts by it once.
	var factsByEntityKey map[string][]int
	factStamp := make([]int, len(out.PriorityFactSeeds))
	// A fact's normalized state slot, computed when the fact is first matched.
	factSlots := make([]string, len(out.PriorityFactSeeds))
	factSlotKnown := make([]bool, len(out.PriorityFactSeeds))
	viewIndex := 0
	for stateIndex := range plan.states {
		state := &plan.states[stateIndex]
		view, subjectKey, subjectWords, subjectPhrase := state.view, state.subjectKey, state.subjectWords, state.subjectPhrase
		viewIndex++
		// Facts that mention the subject, in fact order: the same entity key,
		// a whole source token naming it (Latin name boundaries and Korean
		// inflection as before), or a multi-word name phrase.
		if subjectKey != "" && factsByEntityKey == nil {
			factsByEntityKey = map[string][]int{}
			for i := range out.PriorityFactSeeds {
				if out.PriorityFactSeeds[i].SourceTable == "character_states" {
					continue
				}
				key := prepareTurnPriorityEntityKey(out.PriorityFactSeeds[i].Fact.EntitySurface, out.PriorityEntityAliases)
				factsByEntityKey[key] = append(factsByEntityKey[key], i)
			}
		}
		var mentioning []int
		mark := func(i int) {
			if factStamp[i] != viewIndex {
				factStamp[i] = viewIndex
				mentioning = append(mentioning, i)
			}
		}
		if subjectKey != "" {
			for _, i := range factsByEntityKey[subjectKey] {
				mark(i)
			}
		}
		for _, id := range index.matchingTokensFor(view.Subject, state.subjectKeys) {
			for _, i := range index.factsByToken[id] {
				mark(i)
			}
		}
		if len(subjectWords) > 1 {
			if factsByWord == nil {
				factsByWord = map[string][]int{}
				for i, phrase := range sourcePhrases {
					for _, word := range strings.Fields(phrase) {
						if facts := factsByWord[word]; len(facts) == 0 || facts[len(facts)-1] != i {
							factsByWord[word] = append(facts, i)
						}
					}
				}
			}
			for _, i := range factsByWord[subjectWords[0]] {
				if strings.Contains(sourcePhrases[i], subjectPhrase) {
					mark(i)
				}
			}
		}
		if len(mentioning) == 0 {
			continue
		}
		sort.Ints(mentioning)
		parts := plan.statePartsOf(state)
		for _, i := range mentioning {
			if out.PriorityFactSeeds[i].SourceTable == "character_states" {
				continue // Field-linked current readings already own these projections.
			}
			fact := &out.PriorityFactSeeds[i].Fact
			// A matching name remains discovery/scoring context. It does not make
			// every stored slot part of an explicitly typed assertion. Unknown
			// bindings retain the established reading (including indirect clues);
			// missing optional metadata never removes useful current context.
			if !factSlotKnown[i] && fact.StateSlot != "" {
				factSlots[i], factSlotKnown[i] = normalizeNarrativeStateSlot(fact.StateSlot), true
			}
			linked := fact.StateSlot == "" || factSlots[i] == view.Slot
			for _, field := range state.sourceFields {
				if fact.SourceFieldPath != "" && (fact.SourceFieldPath == field || strings.HasPrefix(fact.SourceFieldPath, strings.TrimRight(field, "/")+"/")) {
					linked = true
				}
			}
			attachments = append(attachments, stateAttachment{i, parts, linked})
		}
	}
	added := map[int]int{}
	for _, attachment := range attachments {
		added[attachment.fact] += len(attachment.parts)
	}
	owned := map[int]*prepareTurnMemoryContext{}
	for _, attachment := range attachments {
		fact := &out.PriorityFactSeeds[attachment.fact].Fact
		reading := owned[attachment.fact]
		if reading == nil {
			reading = &prepareTurnMemoryContext{Path: fact.SourcePath, Parts: []prepareTurnMemoryPart{{Key: fact.SourcePath, Value: fact.Text, FactTexts: []string{fact.Text}}}}
			if fact.Reading != nil {
				*reading = *fact.Reading
				reading.Parts = append(make([]prepareTurnMemoryPart, 0, len(fact.Reading.Parts)+added[attachment.fact]), fact.Reading.Parts...)
			} else {
				reading.Parts = append(make([]prepareTurnMemoryPart, 0, 1+added[attachment.fact]), reading.Parts...)
			}
			reading.fingerprint = [32]byte{}
			owned[attachment.fact] = reading
			fact.Reading = reading
		}
		for _, part := range attachment.parts {
			part.ReferenceOnly = !attachment.linked
			reading.Parts = append(reading.Parts, part)
		}
	}
}

func prepareTurnSourceTemporalContext(base, item map[string]any) map[string]any {
	var out map[string]any
	for _, source := range []map[string]any{base, mapFromAny(item["temporal_context"]), item} {
		for _, key := range []string{"observed_at", "occurrence_time", "relative_expression", "relative"} {
			if value, exists := source[key]; exists {
				if out == nil {
					out = map[string]any{}
				}
				out[key] = value
			}
		}
	}
	return out
}

// Read only the supplied commitment's roles and qualifications. This does not
// resolve names into identities or turn a passed deadline into completion.
// The existing form allocator charges these parts in either selection mode.
func prepareTurnAttachCommitmentRelation(fact *prepareTurnPriorityMemoryFact, record memoryRelationRecord, clock map[string]any) {
	frame := record.Frames[0]
	thread := record.SourceRow.(store.PendingThread)
	prefix := fmt.Sprintf("@commitment/%d/%s", record.Ref.RowID, thread.ThreadKey)
	parts := []prepareTurnMemoryPart{}
	for _, link := range frame.Links {
		parts = append(parts, prepareTurnMemoryPart{Key: prefix + "/" + link.Role, Label: "commitment " + link.Role, Value: link.From.Text})
	}
	// Preserve the stored conditions alongside roles: a date or target alone
	// must not strip the qualification from the obligation.
	for _, key := range []string{"condition", "conditions", "exception", "exceptions", "restriction", "restrictions", "when", "unless", "outcome", "evidence_excerpt"} {
		if value, exists := frame.Fields[key]; exists {
			parts = append(parts, prepareTurnMemoryPart{Key: prefix + "/" + key, Label: "commitment " + key, Value: prepareTurnPriorityScalarText(value)})
		}
	}
	scheduleSource := frame.Fields
	if details := mapFromAny(frame.Fields["lifecycle_details"]); len(details) > 0 {
		scheduleSource = details
	}
	// Copy before adding the current row's lifecycle, so a retained old schedule
	// cannot report an already resolved thread as having an unknown outcome.
	scheduleCopy := make(map[string]any, len(scheduleSource)+1)
	for key, value := range scheduleSource {
		scheduleCopy[key] = value
	}
	scheduleSource = scheduleCopy
	if strings.EqualFold(thread.Status, "resolved") {
		scheduleSource["lifecycle_transition"] = "resolve"
	}
	if schedule := buildCommitmentScheduleReading(scheduleSource, clock); len(schedule) > 0 {
		parts = append(parts, prepareTurnMemoryPart{Key: prefix + "/schedule", Label: "schedule reading (read only)", Value: mustCompactJSON(schedule)})
	}
	if thread.ResolutionNote != "" {
		parts = append(parts, prepareTurnMemoryPart{Key: prefix + "/resolution", Label: "recorded resolution", Value: thread.ResolutionNote})
	}
	if len(parts) == 0 {
		return
	}
	reading := prepareTurnMemoryContext{Path: fact.SourcePath, Parts: []prepareTurnMemoryPart{{Key: fact.SourcePath, Value: fact.Text, FactTexts: []string{fact.Text}}}}
	if fact.Reading != nil {
		reading = *fact.Reading
		reading.Parts = append([]prepareTurnMemoryPart(nil), fact.Reading.Parts...)
	}
	reading.fingerprint = [32]byte{}
	reading.Parts = append(reading.Parts, parts...)
	fact.Reading = &reading
}

// Interpret only source-linked metadata already admitted to this request. The
// helper neither reads additional records nor rewrites the historical text.
func prepareTurnAttachTemporalContext(out *prepareTurnInjectionAssembly, clock map[string]any) {
	prepareTurnAttachTemporalContextWith(out, clock, nil)
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

func prepareTurnAttachTemporalContextWith(out *prepareTurnInjectionAssembly, clock map[string]any, kept *prepareTurnTemporalParts) {
	temporalPart := func(context map[string]any) prepareTurnMemoryPart {
		build := func() prepareTurnMemoryPart {
			value := mustCompactJSON(buildStoryTimeReading(context, clock))
			return prepareTurnMemoryPart{Key: fmt.Sprintf("@temporal/%x", sha256.Sum256([]byte(value))), Label: "source-relative time (last confirmed clock; read only)", Value: value}
		}
		if kept == nil {
			return build()
		}
		var key strings.Builder
		prepareTurnWriteTypedKey(&key, context)
		kept.mu.Lock()
		part, ok := kept.parts[key.String()]
		kept.mu.Unlock()
		if !ok {
			part = build()
			kept.mu.Lock()
			if kept.parts == nil {
				kept.parts = map[string]prepareTurnMemoryPart{}
			}
			kept.parts[key.String()] = part
			kept.mu.Unlock()
		}
		return part
	}
	for i := range out.PriorityFactSeeds {
		fact := &out.PriorityFactSeeds[i].Fact
		if len(fact.TemporalContext) == 0 {
			continue
		}
		reading := prepareTurnMemoryContext{Path: fact.SourcePath, Parts: []prepareTurnMemoryPart{{Key: fact.SourcePath, Value: fact.Text, FactTexts: []string{fact.Text}}}}
		if fact.Reading != nil {
			reading = *fact.Reading
			reading.Parts = append([]prepareTurnMemoryPart(nil), fact.Reading.Parts...)
		}
		reading.fingerprint = [32]byte{}
		reading.Parts = append(reading.Parts, temporalPart(fact.TemporalContext))
		fact.Reading = &reading
	}
}

func prepareTurnAttachLastConfirmedClock(out *prepareTurnInjectionAssembly, clock map[string]any) {
	note := storyTimePromptNote(clock)
	if note == "" {
		return
	}
	out.ContinuityCorrectionText = strings.TrimSpace(out.ContinuityCorrectionText + "\n- " + note)
}

func prepareTurnCharacterFieldPath(path string) string {
	if path == "/state" || strings.HasPrefix(path, "/state/") {
		return "/status" + strings.TrimPrefix(path, "/state")
	}
	return path
}

// Explicit Critic source_fields connect an older field observation to current
// evidence. No relationship between two prose strings is inferred here.
func prepareTurnCharacterFieldCurrentReadings(narrative, reversible []store.StatusCurrentValue, storyClock map[string]any) map[string][]prepareTurnMemoryPart {
	out := map[string][]prepareTurnMemoryPart{}
	appendReading := func(subject string, fields []string, value, transition string, current store.StatusCurrentValue, evidence map[string]any) {
		for _, field := range fields {
			field = prepareTurnCharacterFieldPath(field)
			key := normalizePrepareTurnEntityNeedle(subject) + "\x1f" + field
			prefix := fmt.Sprintf("@field_current/%s/%d", field, current.ID)
			observation := "source turn unknown"
			if current.SourceTurn > 0 {
				observation = fmt.Sprintf("source turn %d", current.SourceTurn)
			}
			parts := []prepareTurnMemoryPart{{Key: prefix, Label: fmt.Sprintf("linked current state [%s; status_current_values:%d]", observation, current.ID), DeliveryLabel: fmt.Sprintf("linked current state [%s]", observation), Value: value}}
			if transition != "" {
				parts = append(parts, prepareTurnMemoryPart{Key: prefix + "/transition", Label: "current transition", Value: transition})
			}
			if excerpt := stringFromMap(evidence, "evidence_excerpt"); excerpt != "" {
				parts = append(parts, prepareTurnMemoryPart{Key: prefix + "/evidence", Label: "current state evidence", Value: excerpt})
			}
			for _, name := range []string{"source_revision", "direct_evidence_ids", "observed_at", "occurrence_time", "effective_time", "validity", "repair_source_revision", "repair_recorded_turn"} {
				if raw, exists := evidence[name]; exists && raw != nil {
					parts = append(parts, prepareTurnMemoryPart{Key: prefix + "/" + name, Label: "current " + name, Value: prepareTurnPriorityScalarText(raw)})
				}
			}
			out[key] = append(out[key], parts...)
		}
	}
	for _, view := range narrativeCurrentStateViews(narrative) {
		switch view.Scope {
		case "belief", "rumor", "secret":
			continue
		}
		origin, evidence := prepareTurnCurrentStateReadingOrigin(view.Value)
		for _, key := range []string{"observed_at", "occurrence_time", "effective_time", "validity"} {
			if value, exists := view.Payload[key]; exists {
				evidence[key] = value
			}
		}
		appendReading(view.Subject, stringsFromAny(view.Payload["source_fields"]), view.Current, stringFromMap(view.Payload, "transition"), origin, evidence)
	}
	subjectSlotCounts := reversibleSubjectSlotCounts(reversible)
	for _, current := range reversible {
		projection := parseJSONMap(current.ValueJSON)
		if stringFromMap(projection, "version") != reversibleStateContractVersion {
			continue
		}
		subject := stringFromMap(projection, "subject_label")
		current, evidence := prepareTurnCurrentStateReadingOrigin(current)
		appendSlot := func(slot map[string]any, transition string, source map[string]any) {
			// Reuse the existing public reversible-state delivery boundary.
			if reversiblePublicDeliveryExclusion(slot, storyClock) != "" {
				return
			}
			text := stringFromMap(mapFromAny(slot["value"]), "text")
			if transition == "clear" || transition == "recover" {
				text = "The referenced former condition is no longer current (" + transition + ")."
			}
			observation := current
			observation.SourceTurn = intFromAny(source["source_turn"], 0)
			appendReading(subject, stringsFromAny(slot["source_fields"]), text, transition, observation, source)
		}
		slots := mapFromAny(projection["slots"])
		keys := make([]string, 0, len(slots))
		for key := range slots {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			subjectSlotKey := strings.Join([]string{stringFromMap(projection, "domain"), normalizePrepareTurnEntityNeedle(subject), key}, "\x1f")
			if subjectSlotCounts[subjectSlotKey] > 1 {
				continue
			}
			slot := mapFromAny(slots[key])
			source := mapFromAny(slot["source"])
			appendSlot(slot, "", source)
		}
		history := mapFromAny(evidence["history_observation"])
		transition := stringFromMap(history, "transition")
		if transition == "clear" || transition == "recover" {
			appendSlot(history, transition, evidence)
		}
	}
	return out
}

func prepareTurnAttachCharacterFieldContext(out *prepareTurnInjectionAssembly, start int, state store.CharacterState, current map[string][]prepareTurnMemoryPart) {
	fields := store.DecodeCharacterFieldProvenance(state.FieldProvenanceJSON)
	for i := start; i < len(out.PriorityFactSeeds); i++ {
		seed := &out.PriorityFactSeeds[i]
		path := prepareTurnCharacterFieldPath(seed.Fact.SourceFieldPath)
		if path == "" {
			path = prepareTurnCharacterFieldPath(seed.Fact.SourcePath)
		}
		provenance := store.CharacterFieldProvenanceForPath(fields, path)
		seed.Fact.ExplicitRefs = appendUniqueStringValues(seed.Fact.ExplicitRefs, prepareTurnKnowledgeRefs(provenance)...)
		// Split readings use /state while the stored field owner uses /status.
		// Read metadata only from this exact containing field, never a sibling.
		var containing any = map[string]any{"status": parseJSONMap(state.StatusJSON), "personality": parseJSONMap(state.PersonalityJSON), "appearance": parseJSONMap(state.AppearanceJSON), "relationships": parseJSONMap(state.RelationshipsJSON)}
		pieces := strings.Split(strings.TrimPrefix(path, "/"), "/")
		for _, piece := range pieces[:maxInt(len(pieces)-1, 0)] {
			piece = strings.ReplaceAll(strings.ReplaceAll(piece, "~1", "/"), "~0", "~")
			switch value := containing.(type) {
			case map[string]any:
				containing = value[piece]
			case []any:
				index := intFromAny(piece, -1)
				if index >= 0 && index < len(value) {
					containing = value[index]
				} else {
					containing = nil
				}
			default:
				containing = nil
			}
		}
		seed.Fact.ExplicitRefs = appendUniqueStringValues(seed.Fact.ExplicitRefs, prepareTurnKnowledgeRefs(mapFromAny(containing))...)
		seed.Fact.TemporalContext = prepareTurnSourceTemporalContext(nil, provenance)
		seed.FieldObservationTurn = intFromAny(provenance["source_turn"], 0)
		seed.ProjectionSource += ":field_provenance"
		reading := prepareTurnMemoryContext{Path: seed.Fact.SourcePath, Parts: []prepareTurnMemoryPart{{Key: seed.Fact.SourcePath, Value: seed.Fact.Text, FactTexts: []string{seed.Fact.Text}}}}
		if seed.Fact.Reading != nil {
			reading = *seed.Fact.Reading
			reading.Parts = append([]prepareTurnMemoryPart(nil), seed.Fact.Reading.Parts...)
		}
		reading.fingerprint = [32]byte{}
		observation := "unknown"
		if seed.FieldObservationTurn > 0 {
			observation = fmt.Sprintf("source turn %d", seed.FieldObservationTurn)
		}
		reading.Parts = append(reading.Parts, prepareTurnMemoryPart{Key: "@field_observation/" + path, Label: "field observation " + path, Value: observation}, prepareTurnMemoryPart{Key: "@field_snapshot/" + path, Label: "containing snapshot", Value: fmt.Sprintf("turn %d (does not date this field)", state.TurnIndex)})
		for _, name := range []string{"source_session_id", "source_revision", "recorded_turn", "evidence_excerpt", "evidence_refs", "direct_evidence_ids", "occurrence_time", "learned_time"} {
			if value, exists := provenance[name]; exists && value != nil {
				reading.Parts = append(reading.Parts, prepareTurnMemoryPart{Key: "@field/" + path + "/" + name, Label: name, Value: prepareTurnPriorityScalarText(value)})
			}
		}
		effective := "unknown"
		if value, exists := provenance["effective_time"]; exists && value != nil {
			effective = prepareTurnPriorityScalarText(value)
		}
		reading.Parts = append(reading.Parts, prepareTurnMemoryPart{Key: "@field/" + path + "/effective", Label: "effective time", Value: effective})
		for linkedPath := path; linkedPath != ""; {
			if parts := current[normalizePrepareTurnEntityNeedle(state.CharacterName)+"\x1f"+linkedPath]; len(parts) > 0 {
				reading.Parts = append(reading.Parts, prepareTurnMemoryPart{Key: "@field/" + path + "/historical", Label: "field use", Value: "Historical observation; use the linked current state and evidence for present continuity."})
				reading.Parts = append(reading.Parts, parts...)
				break
			}
			at := strings.LastIndex(linkedPath, "/")
			if at <= 0 {
				break
			}
			linkedPath = linkedPath[:at]
		}
		seed.Fact.Reading = &reading
	}
}

// Keep retrieval fragments and IDs, but read a typed relation (including an
// abbreviation such as "No. 42") or a full-summary projection as one assertion.
func prepareTurnAttachWholeSourceContext(facts []prepareTurnPriorityMemoryFact, path, text string) []prepareTurnPriorityMemoryFact {
	copyFacts := append([]prepareTurnPriorityMemoryFact(nil), facts...)
	part := prepareTurnMemoryPart{Key: path, Value: text}
	for _, f := range facts {
		part.FactTexts = append(part.FactTexts, f.Text)
	}
	reading := &prepareTurnMemoryContext{Path: path, Parts: []prepareTurnMemoryPart{part}}
	for i := range copyFacts {
		copyFacts[i].Reading = reading
	}
	return copyFacts
}

func prepareTurnAttachRecollectionContext(out *prepareTurnInjectionAssembly, start int, text, evidence string) {
	if start >= len(out.PriorityFactSeeds) {
		return
	}
	seeds := out.PriorityFactSeeds[start:]
	memberTexts := make([]string, 0, len(seeds))
	for _, seed := range seeds {
		memberTexts = append(memberTexts, seed.Fact.Text)
	}
	part := prepareTurnMemoryPart{Key: "/memory_text", Label: "recollection", Value: text, FactTexts: memberTexts}
	reading := &prepareTurnMemoryContext{Path: "/", Label: "Personal recollection", Parts: []prepareTurnMemoryPart{part}}
	for i := range seeds {
		seeds[i].Fact.Reading = reading
		seeds[i].Fact.SourcePath = "/memory_text"
	}
	if evidence = strings.TrimSpace(evidence); evidence != "" {
		seed := seeds[0]
		value := "evidence_excerpt: " + evidence
		seed.Fact = prepareTurnPriorityMemoryFact{Text: value, FamilyKey: collapseTextKey(value), ValueKey: collapseTextKey(evidence), SourcePath: "/evidence_excerpt",
			Reading: &prepareTurnMemoryContext{Path: reading.Path, Label: reading.Label, Parts: []prepareTurnMemoryPart{part, {Key: "/evidence_excerpt", Label: "evidence_excerpt", Value: evidence, FactTexts: []string{value}}}}}
		out.PriorityFactSeeds = append(out.PriorityFactSeeds, seed)
	}
	for i := start; i < len(out.PriorityFactSeeds); i++ {
		out.PriorityFactSeeds[i].SourceFactCount = len(out.PriorityFactSeeds) - start
	}
}

func prepareTurnAttachWorldScope(out *prepareTurnInjectionAssembly, start int, label, scope, name string) {
	for i := start; i < len(out.PriorityFactSeeds); i++ {
		fact := &out.PriorityFactSeeds[i].Fact
		reading := fact.Reading
		if reading == nil {
			reading = &prepareTurnMemoryContext{Path: fact.SourcePath, Label: label, Parts: []prepareTurnMemoryPart{{Key: fact.SourcePath, Value: fact.Text, FactTexts: []string{fact.Text}}}}
			// A lone, opaque fact keeps its original text. Scope is additive metadata.
		}
		copyReading := *reading
		copyReading.fingerprint = [32]byte{}
		copyReading.Parts = append([]prepareTurnMemoryPart(nil), reading.Parts...)
		for _, p := range []prepareTurnMemoryPart{{Key: "@source_scope", Label: "scope", Value: scope}, {Key: "@source_scope_name", Label: "scope_name", Value: name}} {
			if p.Value == "" {
				continue
			}
			same := false
			for _, old := range copyReading.Parts {
				if old.Value == p.Value {
					same = true
					break
				}
			}
			if !same {
				copyReading.Parts = append(copyReading.Parts, p)
			}
		}
		if len(copyReading.Parts) > 1 {
			fact.Reading = &copyReading
		}
	}
}

func prepareTurnMemoryModelCandidate(c prepareTurnPriorityMemoryCandidate, refs map[string]string) map[string]any {
	item := map[string]any{"ref": refs[c.CanonicalFactID], "id": c.CanonicalFactID, "source_ref": c.SourceRef, "source_table": c.SourceTable, "text": prepareTurnMemoryReadingText(c), "source_turn": c.SourceTurn, "visibility": c.Visibility, "perspective_owner": c.PerspectiveOwner, "allowed_viewers": c.AllowedViewers}
	if len(c.KnowledgeBoundaries) > 0 {
		item["knowledge_boundaries"] = c.KnowledgeBoundaries
	}
	if c.Minimum != nil {
		// Keep diagnostics intact; the model packet can share typed linked state
		// without parsing arbitrary story text or merging candidate identities.
		linked := []map[string]any{}
		original := []string{c.Minimum.Heading}
		for _, part := range c.Minimum.Parts {
			if strings.HasPrefix(part.Key, "@current/") {
				linked = append(linked, map[string]any{"key": part.Key, "text": part.Text})
			} else {
				original = append(original, "  "+part.Text)
			}
		}
		if len(linked) > 0 {
			item["original_reading"] = strings.TrimSpace(strings.Join(original, "\n"))
			item["linked_state"] = linked
		}
		contextRefs := []string{}
		for _, id := range c.Minimum.Refs {
			if ref := refs[id]; ref != "" {
				contextRefs = append(contextRefs, ref)
			}
		}
		item["context_refs"] = contextRefs
		item["minimum_chars"] = utf8.RuneCountInString("- " + prepareTurnMemorySourceHeading(c, false) + " " + c.Minimum.Text)
	}
	return item
}

func prepareTurnMemorySourceHeading(c prepareTurnPriorityMemoryCandidate, delivery bool) string {
	parts := []string{}
	if c.SourceTable == "character_states" && strings.HasSuffix(c.ProjectionSource, ":field_provenance") && c.SourceTurn > 0 {
		parts = append(parts, fmt.Sprintf("field observation turn %d", c.SourceTurn))
	} else if c.SourceTable == "character_states" && c.SourceTurn > 0 {
		parts = append(parts, fmt.Sprintf("state snapshot turn %d; fields may be older", c.SourceTurn))
	} else if c.SourceTurn > 0 {
		parts = append(parts, fmt.Sprintf("source turn %d", c.SourceTurn))
	}
	if !delivery {
		parts = append(parts, c.SourceRef)
	} else if len(parts) == 0 {
		parts = append(parts, "source turn unknown")
	}
	if c.PerspectiveOwner != "" {
		parts = append(parts, "owner "+c.PerspectiveOwner)
	}
	if c.Visibility != "" && c.Visibility != "general" && c.Visibility != "public_projection" {
		parts = append(parts, "visibility "+c.Visibility)
	}
	if len(c.AllowedViewers) > 0 && !(len(c.AllowedViewers) == 1 && c.AllowedViewers[0] == c.PerspectiveOwner) {
		parts = append(parts, "viewers "+strings.Join(c.AllowedViewers, ", "))
	}
	return "[" + strings.Join(parts, "; ") + "]"
}

type prepareTurnMemoryReadingRow struct {
	FactID                              string
	Order                               int
	Group, Header, Ref, Plain, rendered string
	Parts                               []prepareTurnMemoryFormPart
	partRefs                            map[prepareTurnMemoryPartIdentity]string
	renderPending                       bool // rendered is built on apply
	// Kept for the next preview of the group: its parts' texts by key, built
	// on first use for a row with many parts, and the rune fold of its
	// rendered text through its last part.
	partIndex map[string][]string
	cost      prepareTurnRowCost
	// distinctParts, when it is the very slice in Parts, is known to hold no
	// two parts with the same key and text.
	distinctParts []prepareTurnMemoryFormPart
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

// hasPart reports whether the row already has a part with this key and text.
// The index is a cache of the row's parts, which never change once the row
// is in the layout.
func (row *prepareTurnMemoryReadingRow) hasPart(part prepareTurnMemoryFormPart) bool {
	return prepareTurnPartsHave(row.Parts, &row.partIndex, part)
}

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

type prepareTurnMemoryPartIdentity struct {
	Key, Text string
}

func prepareTurnMemorySharedPartIdentity(part prepareTurnMemoryFormPart) prepareTurnMemoryPartIdentity {
	key := part.SharedKey
	if strings.HasPrefix(part.Key, "@current/") || strings.HasPrefix(part.Key, "@lifecycle/") || strings.HasPrefix(part.Key, "@field_current/") {
		key = part.Key
	}
	return prepareTurnMemoryPartIdentity{key, part.Text}
}

type prepareTurnMemoryReadingLayout struct {
	rows         []*prepareTurnMemoryReadingRow
	groups       map[string]*prepareTurnMemoryReadingRow
	currentParts map[prepareTurnMemoryPartIdentity]bool
	// currentLookup, when set (shared by the layouts that share
	// currentParts), answers currentParts lookups by the part strings'
	// storage. currentParts only gains entries, so a found part stays found
	// and a missing one is rechecked after any part is added.
	currentLookup *prepareTurnCurrentPartLookup
	// Rune measurements of part texts, keyed by the text's storage; parts
	// shared by many rows are measured once.
	textRunes map[prepareTurnTextStorage]prepareTurnTextRunes
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

// text is the row's delivered text, rendering it if the preview deferred it.
func (row prepareTurnMemoryReadingRow) text() string {
	if row.renderPending {
		return renderPrepareTurnMemoryRow(row)
	}
	return row.rendered
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

type prepareTurnMemoryReadingEdit struct {
	delta    int
	row      prepareTurnMemoryReadingRow
	previous *prepareTurnMemoryReadingRow
	// The previous row's part count when previewed.
	previousParts int
}

// Reuse source-scoped constituents in the group's first visible reading. A later
// selection adds only its new fields there, even across intervening memories.
// Preview is side-effect free: rejected budget reservations cannot consume text.
func (l *prepareTurnMemoryReadingLayout) preview(row prepareTurnMemoryReadingRow) prepareTurnMemoryReadingEdit {
	previous := l.groups[row.Group]
	if row.Group == "" {
		previous = nil
		row.rendered = strings.TrimSpace(row.Plain)
	} else {
		incoming, ref := row.Parts, row.Ref
		incomingDistinct := len(incoming) > 0 && len(row.distinctParts) == len(incoming) && &row.distinctParts[0] == &incoming[0]
		row.distinctParts = nil
		row.Parts = nil
		row.partIndex = nil
		// A row's refs are never changed once set: a preview shares the previous
		// row's and copies them only to add its own.
		row.partRefs = nil
		partCapacity := len(incoming)
		previousParts := 0
		if previous != nil {
			partCapacity += len(previous.Parts)
			previousParts = len(previous.Parts)
		}
		appendPart := func(part prepareTurnMemoryFormPart) {
			if row.Parts == nil {
				row.Parts = make([]prepareTurnMemoryFormPart, 0, partCapacity)
			}
			row.Parts = append(row.Parts, part)
		}
		// A part is already in the row when the previous row has it (read
		// through that row's cached index), or it was added in this preview.
		// Added parts are the tail of row.Parts after the previous row's.
		var addedIndex map[string][]string
		isSeen := func(part prepareTurnMemoryFormPart) bool {
			if previous != nil && previous.hasPart(part) {
				return true
			}
			if previous == nil && incomingDistinct {
				return false // added parts come from incoming, which has no repeats
			}
			var added []prepareTurnMemoryFormPart
			if len(row.Parts) > previousParts {
				added = row.Parts[previousParts:]
			}
			return prepareTurnPartsHave(added, &addedIndex, part)
		}
		if previous != nil {
			row.Order = minInt(row.Order, previous.Order)
			row.Header = previous.Header
			for _, part := range previous.Parts {
				appendPart(part)
			}
			row.partRefs = previous.partRefs
		}
		for _, part := range incoming {
			shared := prepareTurnMemorySharedPartIdentity(part)
			if shared.Key != "" && l.isCurrentPart(shared) {
				continue // Same source constituent already present in this input.
			}
			if !isSeen(part) {
				appendPart(part)
				if addedIndex != nil {
					addedIndex[part.Key] = append(addedIndex[part.Key], part.Text)
				}
				if ref != "" {
					refs := make(map[prepareTurnMemoryPartIdentity]string, len(row.partRefs)+1)
					for key, value := range row.partRefs {
						refs[key] = value
					}
					refs[prepareTurnMemoryPartIdentity{part.Key, part.Text}], ref = ref, ""
					row.partRefs = refs
				}
			}
		}
		// The previous row's parts keep their refs, so its fold carries over.
		if previous != nil && previous.cost.valid {
			row.cost = l.foldRowCost(row, previous.cost, previousParts)
		} else {
			row.cost = l.foldRowCost(row, prepareTurnRowCost{}, 0)
		}
	}
	cost := func(text string) int {
		if text == "" {
			return 0
		}
		return 1 + utf8.RuneCountInString(text)
	}
	var delta int
	if row.Group == "" {
		delta = cost(row.rendered)
	} else if measured, ok := row.cost.renderedCost(len(row.Parts)); ok {
		// Most previewed rows are rejected by the budget; render on apply.
		delta, row.rendered, row.renderPending = measured, "", true
	} else {
		row.rendered = renderPrepareTurnMemoryRow(row)
		delta = cost(row.rendered)
	}
	if previous != nil {
		delta -= cost(previous.rendered)
	}
	edit := prepareTurnMemoryReadingEdit{delta: delta, row: row, previous: previous}
	if previous != nil {
		edit.previousParts = len(previous.Parts)
	}
	return edit
}
func (l *prepareTurnMemoryReadingLayout) apply(edit prepareTurnMemoryReadingEdit) string {
	edit.row.rendered, edit.row.renderPending = edit.row.text(), false
	row := &edit.row
	if edit.previous != nil {
		oldOrder := edit.previous.Order
		// The new row is the previous row's parts followed by the added ones;
		// the previous row is overwritten, so its part index carries over.
		if index := edit.previous.partIndex; index != nil && edit.previousParts == len(edit.previous.Parts) {
			for _, part := range edit.row.Parts[len(edit.previous.Parts):] {
				index[part.Key] = append(index[part.Key], part.Text)
			}
			edit.row.partIndex = index
		}
		*edit.previous = edit.row
		row = edit.previous
		if row.Order != oldOrder {
			// Only an earlier borrowed selection changes group placement.
			sort.SliceStable(l.rows, func(i, j int) bool { return l.rows[i].Order < l.rows[j].Order })
		}
	} else {
		pos := sort.Search(len(l.rows), func(i int) bool { return l.rows[i].Order > row.Order })
		l.rows = append(l.rows, nil)
		copy(l.rows[pos+1:], l.rows[pos:])
		l.rows[pos] = row
	}
	if row.Group != "" {
		if l.groups == nil {
			l.groups = map[string]*prepareTurnMemoryReadingRow{}
		}
		l.groups[row.Group] = row
	}
	for _, part := range row.Parts {
		shared := prepareTurnMemorySharedPartIdentity(part)
		if shared.Key != "" && l.currentParts != nil && !l.currentParts[shared] {
			l.currentParts[shared] = true
			if l.currentLookup != nil {
				l.currentLookup.version++
			}
		}
	}
	return row.rendered
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

func (l *prepareTurnMemoryReadingLayout) texts() []string {
	lines := make([]string, 0, len(l.rows))
	for _, row := range l.rows {
		if row.rendered != "" {
			lines = append(lines, row.rendered)
		}
	}
	return lines
}
func prepareTurnMemoryCandidateRow(c prepareTurnPriorityMemoryCandidate, order int, ref string) prepareTurnMemoryReadingRow {
	return prepareTurnMemoryCandidateRowWithPlain(c, order, ref, "- "+c.RenderedText)
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

func prepareTurnMemoryCandidateRowWithParts(c prepareTurnPriorityMemoryCandidate, order int, ref, plain string, parts []prepareTurnMemoryFormPart) prepareTurnMemoryReadingRow {
	row := prepareTurnMemoryReadingRow{Order: order, FactID: c.CanonicalFactID, Plain: plain}
	if c.Minimum != nil {
		row.Group, row.Header, row.Parts, row.Ref = c.Minimum.Group, strings.TrimSpace(prepareTurnMemorySourceHeading(c, true)+" "+c.Minimum.Heading), parts, ref
	}
	return row
}

// Project only at final assembly, before layout merging and char accounting.
// Summary forms copy these same parts, preserving their delivery overrides.
func prepareTurnMemoryDeliveryParts(parts []prepareTurnMemoryFormPart) []prepareTurnMemoryFormPart {
	out := make([]prepareTurnMemoryFormPart, 0, len(parts))
	for _, part := range parts {
		if part.DeliveryText != nil {
			part.Text = *part.DeliveryText
			if part.Text == "" {
				continue
			}
		}
		out = append(out, part)
	}
	return out
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
