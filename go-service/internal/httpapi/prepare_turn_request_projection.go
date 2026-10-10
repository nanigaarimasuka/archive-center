package httpapi

import (
	"crypto/sha256"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unsafe"

	"github.com/risulongmemory/archive-center-go/internal/store"
)

// This is an evaluation workspace, never a session cache. The route owns it
// until the request ends. Source values and independent query evidence remain
// separate; new search scores are applied after the reusable source conversion.
type prepareTurnRequestPreparation struct {
	metrics       *prepareTurnMeasurement
	baseQueries   []string
	baseLexical   *prepareTurnRelevanceMemo
	seedTemplates map[prepareTurnSourceTemplateKey]prepareTurnSourceTemplate
	payloads      map[string]any
	surfaces      map[string]string
	facts         map[string][]prepareTurnPriorityMemoryFact
	recall        map[store.Memory]prepareTurnRecallMemory
	summaries     map[store.Memory]string
	memorySeeds   map[store.Memory][]prepareTurnPriorityFactSeed
	guards        map[prepareTurnGuardKey]prepareTurnProtectedMemoryGuardResult
	lexical       map[string]*prepareTurnRelevanceMemo
	lexicalTexts  map[string]prepareTurnPriorityLexicalText
	lexParts      map[string]*prepareTurnLexicalPart
	lexFields     map[string]*prepareTurnLexicalField
	lexIDs        map[string]int32
	partNeedles   map[string]prepareTurnNeedleSegment
	// The same caches by the storage of the part string: parts are mostly
	// shared strings, found without hashing their (long) contents.
	lexPartsByStorage    map[prepareTurnTextStorage]*prepareTurnLexicalPart
	partNeedlesByStorage map[prepareTurnTextStorage]prepareTurnNeedleSegment
	partDigests          map[prepareTurnPartDigestKey][32]byte
	// Digests by the storage of a part's strings, confirmed by content.
	partDigestsByStorage map[uint64][]prepareTurnStoredPartDigest
	readingForms         map[prepareTurnMemoryFormKey]*prepareTurnMemoryForm
	readingGroups        map[prepareTurnMemoryGroupKey]string
	matches              map[string]func(store.Memory) prepareTurnRecallEvidence
	similarities         map[string]map[store.Memory]float64
	// Forms kept across requests; nil when they are not reused.
	sharedReadingForms *readingFormCache
	// Scored texts are long; each is hashed once to an id the memos share.
	scoredTexts prepareTurnTextIDs
}

func newPrepareTurnRequestPreparation(common *prepareTurnAssemblyCommon) *prepareTurnRequestPreparation {
	recall := make(map[store.Memory]prepareTurnRecallMemory, len(common.RecallMemories))
	for item, value := range common.RecallMemories {
		recall[item] = value
	}
	return &prepareTurnRequestPreparation{
		seedTemplates: map[prepareTurnSourceTemplateKey]prepareTurnSourceTemplate{},
		payloads:      map[string]any{}, surfaces: map[string]string{}, facts: map[string][]prepareTurnPriorityMemoryFact{},
		recall: recall, summaries: map[store.Memory]string{},
		memorySeeds: map[store.Memory][]prepareTurnPriorityFactSeed{},
		guards:      map[prepareTurnGuardKey]prepareTurnProtectedMemoryGuardResult{},
		lexical:     map[string]*prepareTurnRelevanceMemo{}, matches: map[string]func(store.Memory) prepareTurnRecallEvidence{},
		lexicalTexts: map[string]prepareTurnPriorityLexicalText{},
		lexParts:     map[string]*prepareTurnLexicalPart{}, lexFields: map[string]*prepareTurnLexicalField{},
		lexIDs: map[string]int32{}, partNeedles: map[string]prepareTurnNeedleSegment{},
		lexPartsByStorage:    map[prepareTurnTextStorage]*prepareTurnLexicalPart{},
		partNeedlesByStorage: map[prepareTurnTextStorage]prepareTurnNeedleSegment{},
		partDigests:          map[prepareTurnPartDigestKey][32]byte{},
		partDigestsByStorage: map[uint64][]prepareTurnStoredPartDigest{},
		readingForms:         map[prepareTurnMemoryFormKey]*prepareTurnMemoryForm{}, readingGroups: map[prepareTurnMemoryGroupKey]string{},
		similarities: map[string]map[store.Memory]float64{},
		scoredTexts:  prepareTurnTextIDs{ids: map[string]int32{}},
	}
}

// prepareTurnTextIDs numbers texts in first-seen order. A text whose storage
// was seen before is found without hashing its contents.
type prepareTurnTextIDs struct {
	ids       map[string]int32
	byStorage map[prepareTurnTextStorage]int32
}

func (t *prepareTurnTextIDs) id(text string) int32 {
	storage := prepareTurnStorageOf(text)
	if id, ok := t.byStorage[storage]; ok {
		return id
	}
	id, ok := t.ids[text]
	if !ok {
		id = int32(len(t.ids))
		t.ids[text] = id
	}
	if t.byStorage == nil {
		t.byStorage = map[prepareTurnTextStorage]int32{}
	}
	t.byStorage[storage] = id
	return id
}

type prepareTurnGuardKey struct {
	item store.Memory
	pov  string
}

func (p *prepareTurnRequestPreparation) sourceMap(raw string) map[string]any {
	if p == nil {
		return parseJSONMap(raw)
	}
	value, ok := p.payload(raw).(map[string]any)
	if !ok || value == nil {
		return map[string]any{}
	}
	return value
}

func (p *prepareTurnRequestPreparation) guard(item store.Memory, perspective ...map[string]any) prepareTurnProtectedMemoryGuardResult {
	if p == nil {
		return prepareTurnProtectedMemoryGuard(item, perspective...)
	}
	key := prepareTurnGuardKey{item: item}
	if len(perspective) > 0 {
		key.pov = mustCompactJSON(perspective[0])
	}
	if value, ok := p.guards[key]; ok {
		return value
	}
	value := prepareTurnProtectedMemoryGuardFromParsed(p.sourceMap(item.SummaryJSON), perspective...)
	p.guards[key] = value
	return value
}

func (p *prepareTurnRequestPreparation) entityMatches(item store.Memory, entities []string) []string {
	return prepareTurnMemoryEntityMatches(p.summary(item), p.recallSource(item).anchors, entities)
}

type prepareTurnSourceTemplateKey struct {
	source  [32]byte
	query   string
	reading [32]byte
}
type prepareTurnSourceTemplate struct {
	candidate prepareTurnPriorityMemoryCandidate
	metadata  *prepareTurnPriorityIdentityMetadata
}

func prepareTurnSourceSeedKey(seed prepareTurnPriorityFactSeed, query string) prepareTurnSourceTemplateKey {
	return prepareTurnSourceSeedKeyWith(seed, query, seed.Fact.Reading.sourceFingerprint())
}

func (p *prepareTurnRequestPreparation) sourceSeedKey(seed prepareTurnPriorityFactSeed, query string) prepareTurnSourceTemplateKey {
	return prepareTurnSourceSeedKeyWith(seed, query, p.readingFingerprint(seed.Fact.Reading))
}

func prepareTurnSourceSeedKeyWith(seed prepareTurnPriorityFactSeed, query string, readingFingerprint [32]byte) prepareTurnSourceTemplateKey {
	// Query discoveries are observations on the source, not its identity.
	seed.SourceSelectionScore = 0
	seed.SourceSelectionScoreObserved, seed.SourceSelectionScoreIsVector = false, false
	seed.RecallQueries = nil
	seed.SemanticSimilarity, seed.SemanticSimilarityObserved = 0, false
	seed.SemanticUnitID, seed.SemanticSimilaritySource = "", ""
	encoded, _ := json.Marshal(seed)
	return prepareTurnSourceTemplateKey{sha256.Sum256(encoded), query, readingFingerprint}
}

// Callers that edit a payload must copy its map first (canonical world-state
// presentation removes displayed rules locally). The saved source is immutable.
func (p *prepareTurnRequestPreparation) payload(raw string) any {
	if value, ok := p.payloads[raw]; ok {
		return value
	}
	defer p.measurement().start("assembly.row_parse").end()
	value := parseSurfacePayload(raw)
	p.payloads[raw] = value
	return value
}

func (p *prepareTurnRequestPreparation) surface(raw string) string {
	if value, ok := p.surfaces[raw]; ok {
		return value
	}
	value := prepareTurnSurfaceText(p.payload(raw))
	p.surfaces[raw] = value
	return value
}

func prepareTurnPreparedSplitFact(out *prepareTurnInjectionAssembly, line string) []prepareTurnPriorityMemoryFact {
	if out.preparation == nil {
		return prepareTurnPrioritySplitFact(line)
	}
	p := out.preparation
	if facts, ok := p.facts[line]; ok {
		return facts
	}
	defer p.measurement().start("assembly.fact_parse").end()
	facts := prepareTurnPrioritySplitFact(line)
	p.facts[line] = facts
	return facts
}

func (p *prepareTurnRequestPreparation) similarity(query string, item store.Memory) float64 {
	values := p.similarities[query]
	if values == nil {
		values = map[store.Memory]float64{}
		p.similarities[query] = values
	}
	if value, ok := values[item]; ok {
		return value
	}
	value := simpleTokenSimilarity(query, p.recallSource(item).text)
	values[item] = value
	return value
}

func (p *prepareTurnRequestPreparation) summary(item store.Memory) string {
	if p == nil {
		return prepareTurnMemorySummary(item)
	}
	if value, ok := p.summaries[item]; ok {
		return value
	}
	value := prepareTurnMemorySummary(item)
	p.summaries[item] = value
	return value
}

func (p *prepareTurnRequestPreparation) recallSource(item store.Memory) prepareTurnRecallMemory {
	if value, ok := p.recall[item]; ok {
		return value
	}
	value := prepareTurnPrepareRecallMemory(item)
	p.recall[item] = value
	return value
}

func (p *prepareTurnRequestPreparation) recallMatcher(query string) func(store.Memory) prepareTurnRecallEvidence {
	if fn, ok := p.matches[query]; ok {
		return fn
	}
	match := prepareTurnMemoryRecallMatcher(query)
	values := map[store.Memory]prepareTurnRecallEvidence{}
	fn := func(item store.Memory) prepareTurnRecallEvidence {
		value, ok := values[item]
		if !ok {
			value = match(p.recallSource(item))
			values[item] = value
		}
		// The selector extends overlap evidence while merging independent queries.
		value.OverlapTerms = append([]string(nil), value.OverlapTerms...)
		return value
	}
	p.matches[query] = fn
	return fn
}

// Readings are their parts joined by "\n", and many readings share the same
// long parts. Recall fields never cross that separator, so the analysis of the
// joined text is composed from cached per-part fields using the same first-seen
// order as prepareTurnRecallTerms, and the needle from per-part needle
// characters with the whole text's surrounding white space removed. The result
// equals prepareTurnPriorityAnalyzeText(joined); later lookups reuse it.
//
// Shared caches are filled serially in the given order; the per-reading merge
// only reads them and runs concurrently. A meaning that is already analyzed,
// or repeats an earlier item, keeps its first analysis.
func (p *prepareTurnRequestPreparation) primeJoinedLexicalTexts(items []prepareTurnJoinedLexicalText) {
	if p == nil {
		return
	}
	pending := make([]prepareTurnJoinedLexicalText, 0, len(items))
	queued := map[string]bool{}
	for _, item := range items {
		if _, ok := p.lexicalTexts[item.joined]; ok || queued[item.joined] {
			continue
		}
		queued[item.joined] = true
		if item.kept != nil {
			if data := item.kept.data.Load(); data != nil {
				p.lexicalTexts[item.joined] = p.keptLexicalText(data)
				continue
			}
		}
		pending = append(pending, item)
	}
	if len(pending) == 0 {
		return
	}
	started := time.Now()
	// Most parts are already cached. Look each reading's parts up concurrently
	// (reads only), fill the missing ones in reading and part order, then
	// merge from the looked-up entries without hashing the parts again.
	readings := make([]prepareTurnLexicalReading, len(pending))
	prepareTurnParallelFor(len(pending), func(i int) {
		readings[i] = p.lookupLexicalParts(pending[i].parts)
	})
	for i, item := range pending {
		for _, miss := range readings[i].misses {
			p.fillLexicalPart(item.parts[miss.index], miss.needle)
		}
		readings[i].resolve(p, item.parts)
	}
	stamps := sync.Pool{New: func() any { return &prepareTurnLexicalStamp{} }}
	analyzed := make([]prepareTurnPriorityLexicalText, len(pending))
	prepareTurnParallelFor(len(pending), func(i int) {
		stamp := stamps.Get().(*prepareTurnLexicalStamp)
		defer stamps.Put(stamp)
		analyzed[i] = p.joinedLexicalText(pending[i].parts, readings[i], stamp)
	})
	for i, item := range pending {
		p.lexicalTexts[item.joined] = analyzed[i]
		if item.kept != nil {
			item.kept.keep(item.parts, analyzed[i])
		}
	}
	// One tokenize call per analyzed meaning, as when each was primed alone.
	p.measurement().record("assembly.tokenize", len(pending), time.Since(started))
}

// prepareTurnJoinedLexicalText is a reading meaning and the parts it joins.
type prepareTurnJoinedLexicalText struct {
	joined string
	parts  []string
	// kept, when set, keeps the meaning's analysis across requests.
	kept *prepareTurnKeptLexical
}

// prepareTurnKeptLexical keeps a meaning's analysis without request ids.
// Ids only tell equal values apart from others within a request, so each
// request gives the kept terms and shared segments its own ids.
type prepareTurnKeptLexical struct {
	data atomic.Pointer[prepareTurnKeptLexicalData]
}

type prepareTurnKeptLexicalData struct {
	terms []prepareTurnPriorityLexicalTerm
	// segments are the needle segments in order: a shared segment is found
	// by its part, as when the meaning was analyzed; nil when there were none.
	segments []prepareTurnKeptNeedleSegment
}

type prepareTurnKeptNeedleSegment struct {
	text, part string // text for the meaning's own segments, part for shared ones
	shared     bool
}

// keep records a meaning's analysis; the first one kept stays.
func (k *prepareTurnKeptLexical) keep(parts []string, analyzed prepareTurnPriorityLexicalText) {
	data := &prepareTurnKeptLexicalData{terms: append([]prepareTurnPriorityLexicalTerm(nil), analyzed.terms...)}
	if analyzed.needleSegments != nil {
		first, _ := prepareTurnJoinedTextBounds(parts)
		data.segments = make([]prepareTurnKeptNeedleSegment, len(analyzed.needleSegments))
		for j, segment := range analyzed.needleSegments {
			if segment.id >= 0 {
				data.segments[j] = prepareTurnKeptNeedleSegment{part: parts[first+j], shared: true}
			} else {
				data.segments[j] = prepareTurnKeptNeedleSegment{text: segment.text}
			}
		}
	}
	k.data.CompareAndSwap(nil, data)
}

// keptLexicalText is the analysis kept in data, with this request's ids.
func (p *prepareTurnRequestPreparation) keptLexicalText(data *prepareTurnKeptLexicalData) prepareTurnPriorityLexicalText {
	analyzed := prepareTurnPriorityLexicalText{terms: data.terms, termIDs: make([]int32, len(data.terms))}
	for k, term := range data.terms {
		analyzed.termIDs[k] = p.lexicalID(term.value)
	}
	if data.segments != nil {
		analyzed.needleSegments = make([]prepareTurnNeedleSegment, len(data.segments))
		for j, segment := range data.segments {
			if segment.shared {
				p.fillLexicalPart(segment.part, true)
				analyzed.needleSegments[j] = p.partNeedles[segment.part]
			} else {
				analyzed.needleSegments[j] = prepareTurnNeedleSegment{text: segment.text, id: -1}
			}
		}
	}
	return analyzed
}

// A part's distinct recall fields, each with its term forms. Field strings and
// form values share one id space, so a reading tracks what it has seen by id.
type prepareTurnLexicalPart struct {
	fields []*prepareTurnLexicalField
}

type prepareTurnLexicalField struct {
	id    int32
	forms []prepareTurnLexicalForm
}

type prepareTurnLexicalForm struct {
	id   int32
	term prepareTurnPriorityLexicalTerm
}

// A reading's cached entries: part analysis for every part and needle
// segments for middle parts, with what still has to be filled.
type prepareTurnLexicalReading struct {
	parts   []*prepareTurnLexicalPart
	needles []prepareTurnNeedleSegment
	misses  []prepareTurnLexicalPartMiss
	// unstored entries were found by content only; their storage is recorded
	// with the misses, after the concurrent lookups.
	unstored []prepareTurnLexicalPartMiss
}

// A part of one reading that is missing from the shared lexical caches.
type prepareTurnLexicalPartMiss struct {
	index  int
	needle bool // a middle part, read as a shared needle segment
}

// lookupLexicalParts reads the caches only. Misses are listed in the order
// the serial analysis filled them: terms of every part, then middle needles.
func (p *prepareTurnRequestPreparation) lookupLexicalParts(parts []string) prepareTurnLexicalReading {
	reading := prepareTurnLexicalReading{parts: make([]*prepareTurnLexicalPart, len(parts)), needles: make([]prepareTurnNeedleSegment, len(parts))}
	for i, part := range parts {
		if entry, ok := p.lexPartsByStorage[prepareTurnStorageOf(part)]; ok {
			reading.parts[i] = entry
		} else if entry, ok := p.lexParts[part]; ok {
			reading.parts[i] = entry
			reading.unstored = append(reading.unstored, prepareTurnLexicalPartMiss{index: i})
		} else {
			reading.misses = append(reading.misses, prepareTurnLexicalPartMiss{index: i})
		}
	}
	first, last := prepareTurnJoinedTextBounds(parts)
	for i := first + 1; i < last; i++ {
		if segment, ok := p.partNeedlesByStorage[prepareTurnStorageOf(parts[i])]; ok {
			reading.needles[i] = segment
		} else if segment, ok := p.partNeedles[parts[i]]; ok {
			reading.needles[i] = segment
			reading.unstored = append(reading.unstored, prepareTurnLexicalPartMiss{index: i, needle: true})
		} else {
			reading.misses = append(reading.misses, prepareTurnLexicalPartMiss{index: i, needle: true})
		}
	}
	return reading
}

// resolve looks up the entries that were missing, after they were filled.
func (r *prepareTurnLexicalReading) resolve(p *prepareTurnRequestPreparation, parts []string) {
	for _, miss := range r.misses {
		if miss.needle {
			r.needles[miss.index] = p.partNeedles[parts[miss.index]]
		} else {
			r.parts[miss.index] = p.lexParts[parts[miss.index]]
		}
	}
	if p.lexPartsByStorage == nil {
		p.lexPartsByStorage = map[prepareTurnTextStorage]*prepareTurnLexicalPart{}
		p.partNeedlesByStorage = map[prepareTurnTextStorage]prepareTurnNeedleSegment{}
	}
	for _, list := range [][]prepareTurnLexicalPartMiss{r.misses, r.unstored} {
		for _, entry := range list {
			storage := prepareTurnStorageOf(parts[entry.index])
			if entry.needle {
				p.partNeedlesByStorage[storage] = r.needles[entry.index]
			} else {
				p.lexPartsByStorage[storage] = r.parts[entry.index]
			}
		}
	}
	r.misses, r.unstored = nil, nil
}

func prepareTurnStorageOf(text string) prepareTurnTextStorage {
	return prepareTurnTextStorage{unsafe.StringData(text), len(text)}
}

// fillLexicalPart caches a part's terms with their forms, or its shared
// needle segment. An earlier reading may already have filled it.
func (p *prepareTurnRequestPreparation) fillLexicalPart(part string, needle bool) {
	if needle {
		if _, ok := p.partNeedles[part]; !ok {
			p.partNeedles[part] = prepareTurnNeedleSegment{text: prepareTurnEntityNeedleChars(part), id: int32(len(p.partNeedles))}
		}
		return
	}
	if _, ok := p.lexParts[part]; ok {
		return
	}
	entry := &prepareTurnLexicalPart{fields: []*prepareTurnLexicalField{}}
	distinct := map[string]bool{}
	// A repeated field cannot add a form: its own forms were added first.
	for _, field := range strings.FieldsFunc(strings.ToLower(part), prepareTurnRecallTermBreak) {
		if distinct[field] {
			continue
		}
		distinct[field] = true
		cached, ok := p.lexFields[field]
		if !ok {
			cached = &prepareTurnLexicalField{id: p.lexicalID(field)}
			for _, form := range prepareTurnRecallTermForms(field) {
				term := prepareTurnPriorityLexicalTermOf(form)
				cached.forms = append(cached.forms, prepareTurnLexicalForm{id: p.lexicalID(term.value), term: term})
			}
			p.lexFields[field] = cached
		}
		entry.fields = append(entry.fields, cached)
	}
	p.lexParts[part] = entry
}

func (p *prepareTurnRequestPreparation) lexicalID(value string) int32 {
	id, ok := p.lexIDs[value]
	if !ok {
		id = int32(len(p.lexIDs))
		p.lexIDs[value] = id
	}
	return id
}

// prepareTurnLexicalStamp marks the ids seen by the reading being merged:
// an id is seen when its mark equals the current generation.
type prepareTurnLexicalStamp struct {
	marks      []uint32
	generation uint32
	// Reused while merging; each result is copied out at its exact size.
	terms   []prepareTurnPriorityLexicalTerm
	termIDs []int32
}

func (s *prepareTurnLexicalStamp) next(size int) {
	if len(s.marks) < size {
		s.marks = make([]uint32, size)
		s.generation = 0
	}
	s.generation++
	if s.generation == 0 {
		clear(s.marks)
		s.generation = 1
	}
}

// joinedLexicalText merges cached part analysis in first-seen order, as
// prepareTurnRecallTerms orders the joined text. It only reads the caches.
func (p *prepareTurnRequestPreparation) joinedLexicalText(parts []string, reading prepareTurnLexicalReading, stamp *prepareTurnLexicalStamp) prepareTurnPriorityLexicalText {
	stamp.next(len(p.lexIDs))
	seen := func(id int32) bool { return stamp.marks[id] == stamp.generation }
	scratchTerms, scratchIDs := stamp.terms[:0], stamp.termIDs[:0]
	for _, entry := range reading.parts {
		for _, field := range entry.fields {
			if seen(field.id) {
				continue
			}
			for _, form := range field.forms {
				if !seen(form.id) {
					stamp.marks[form.id] = stamp.generation
					scratchTerms = append(scratchTerms, form.term)
					scratchIDs = append(scratchIDs, form.id)
				}
			}
		}
	}
	stamp.terms, stamp.termIDs = scratchTerms, scratchIDs
	terms := append(make([]prepareTurnPriorityLexicalTerm, 0, len(scratchTerms)), scratchTerms...)
	termIDs := append(make([]int32, 0, len(scratchIDs)), scratchIDs...)
	first, last := prepareTurnJoinedTextBounds(parts)
	// The needle stays segmented: middle parts are shared request segments and
	// only the trimmed first and last parts are this reading's own text.
	analyzed := prepareTurnPriorityLexicalText{terms: terms, termIDs: termIDs}
	if first >= 0 {
		analyzed.needleSegments = make([]prepareTurnNeedleSegment, 0, last-first+1)
		for i := first; i <= last; i++ {
			part := parts[i]
			if i != first && i != last {
				analyzed.needleSegments = append(analyzed.needleSegments, reading.needles[i])
				continue
			}
			if i == first {
				part = strings.TrimLeftFunc(part, unicode.IsSpace)
			}
			if i == last {
				part = strings.TrimRightFunc(part, unicode.IsSpace)
			}
			analyzed.needleSegments = append(analyzed.needleSegments, prepareTurnNeedleSegment{text: prepareTurnEntityNeedleChars(part), id: -1})
		}
	}
	return analyzed
}

// First and last parts with non-space text, or -1 when there are none.
func prepareTurnJoinedTextBounds(parts []string) (int, int) {
	first, last := -1, -1
	for i, part := range parts {
		if strings.TrimSpace(part) != "" {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	return first, last
}

// keptReadingForms is nil-safe; it is nil when forms are not reused.
func (p *prepareTurnRequestPreparation) keptReadingForms() *readingFormCache {
	if p == nil {
		return nil
	}
	return p.sharedReadingForms
}

// Same value as sourceFingerprint; digests of parts shared by many readings are
// computed once per request. Without a preparation it hashes directly.
func (p *prepareTurnRequestPreparation) readingFingerprint(c *prepareTurnMemoryContext) [32]byte {
	if p == nil {
		return c.sourceFingerprint()
	}
	return c.fingerprintWith(p.partDigest)
}

// partDigest is prepareTurnMemoryPartDigest(part). A part is usually a copy
// of one seen before, sharing its strings; it is found by their storage and
// confirmed by comparing contents, without hashing or joining long texts.
func (p *prepareTurnRequestPreparation) partDigest(part prepareTurnMemoryPart) [32]byte {
	storage := prepareTurnPartStorageHash(part)
	for _, stored := range p.partDigestsByStorage[storage] {
		if prepareTurnSameMemoryPart(stored.part, part) {
			return stored.digest
		}
	}
	digest := p.partDigestByContent(part)
	// The stored copy owns its fact list: copies of a part share a backing
	// array that a later append may overwrite.
	stored := part
	if part.FactTexts != nil {
		stored.FactTexts = append(make([]string, 0, len(part.FactTexts)), part.FactTexts...)
	}
	p.partDigestsByStorage[storage] = append(p.partDigestsByStorage[storage], prepareTurnStoredPartDigest{stored, digest})
	return digest
}

type prepareTurnStoredPartDigest struct {
	part   prepareTurnMemoryPart
	digest [32]byte
}

// prepareTurnPartStorageHash mixes where a part's strings are stored, not
// what they hold; equal parts in different storage only miss the shortcut.
func prepareTurnPartStorageHash(part prepareTurnMemoryPart) uint64 {
	h := uint64(14695981039346656037)
	mix := func(v uint64) {
		h ^= v
		h *= 1099511628211
		h ^= h >> 29
	}
	str := func(s string) {
		mix(uint64(uintptr(unsafe.Pointer(unsafe.StringData(s)))))
		mix(uint64(len(s)))
	}
	str(part.Key)
	str(part.Label)
	str(part.Value)
	str(part.DeliveryLabel)
	flags := uint64(0)
	if part.ReferenceOnly {
		flags |= 1
	}
	if part.FactTexts == nil {
		flags |= 2
	}
	mix(flags)
	mix(uint64(len(part.FactTexts)))
	for _, text := range part.FactTexts {
		str(text)
	}
	return h
}

func prepareTurnSameMemoryPart(a, b prepareTurnMemoryPart) bool {
	if a.Key != b.Key || a.Label != b.Label || a.Value != b.Value || a.DeliveryLabel != b.DeliveryLabel ||
		a.ReferenceOnly != b.ReferenceOnly || (a.FactTexts == nil) != (b.FactTexts == nil) || len(a.FactTexts) != len(b.FactTexts) {
		return false
	}
	for i := range a.FactTexts {
		if a.FactTexts[i] != b.FactTexts[i] {
			return false
		}
	}
	return true
}

func (p *prepareTurnRequestPreparation) partDigestByContent(part prepareTurnMemoryPart) [32]byte {
	key := prepareTurnPartDigestKey{part.Key, part.Label, part.Value, part.DeliveryLabel, part.ReferenceOnly, part.FactTexts == nil, ""}
	if len(part.FactTexts) > 0 {
		var facts strings.Builder
		for _, text := range part.FactTexts {
			facts.WriteString(strconv.Itoa(len(text)))
			facts.WriteByte(':')
			facts.WriteString(text)
		}
		key.facts = facts.String()
	}
	digest, ok := p.partDigests[key]
	if !ok {
		digest = prepareTurnMemoryPartDigest(part)
		p.partDigests[key] = digest
	}
	return digest
}

// Distinct parts give distinct keys: facts are length-prefixed and nil and
// empty fact lists are kept apart.
type prepareTurnPartDigestKey struct {
	key, label, value, deliveryLabel string
	referenceOnly, factsNil          bool
	facts                            string
}

// nil when the request has no preparation; callers then analyze text directly.
func prepareTurnPreparationLexicalText(p *prepareTurnRequestPreparation) func(string) prepareTurnPriorityLexicalText {
	if p == nil {
		return nil
	}
	return p.lexicalText
}

func (p *prepareTurnRequestPreparation) lexicalText(text string) prepareTurnPriorityLexicalText {
	if value, ok := p.lexicalTexts[text]; ok {
		return value
	}
	defer p.measurement().start("assembly.tokenize").end()
	value := prepareTurnPriorityAnalyzeText(text)
	p.lexicalTexts[text] = value
	return value
}

// A memoized lexical scorer for one query list. Scores are pure, so values
// may be filled ahead of the calls that read them.
type prepareTurnRelevanceMemo struct {
	queries  []string
	fallback string
	score    func(string) float64
	texts    *prepareTurnTextIDs
	// Scores by text id; known marks the ids that have one.
	values []float64
	known  []bool
}

func (m *prepareTurnRelevanceMemo) get(text string) float64 {
	return m.getID(m.texts.id(text), text)
}

func (m *prepareTurnRelevanceMemo) getID(id int32, text string) float64 {
	if m.has(id) {
		return m.values[id]
	}
	value := m.score(text)
	m.set(id, value)
	return value
}

func (m *prepareTurnRelevanceMemo) has(id int32) bool {
	return int(id) < len(m.known) && m.known[id]
}

func (m *prepareTurnRelevanceMemo) set(id int32, value float64) {
	if grow := int(id) + 1 - len(m.known); grow > 0 {
		m.known = append(m.known, make([]bool, grow)...)
		m.values = append(m.values, make([]float64, grow)...)
	}
	m.known[id], m.values[id] = true, value
}

// relevanceMemos resolves the memoized scorers whose maximum is the
// relevance for these queries, creating them as relevanceScorer always has.
func (p *prepareTurnRequestPreparation) relevanceMemos(queries []string, fallback string) []*prepareTurnRelevanceMemo {
	if len(queries) == 0 {
		queries = []string{fallback}
	}
	memos := make([]*prepareTurnRelevanceMemo, 0, len(queries))
	if p.baseLexical == nil {
		p.baseQueries = append([]string{}, queries...)
		p.baseLexical = &prepareTurnRelevanceMemo{queries: p.baseQueries, fallback: fallback, texts: &p.scoredTexts}
		p.baseLexical.score = prepareTurnPriorityRelevanceScorer(p.baseQueries, fallback, p.lexicalText)
	}
	// Preserve one combined base evaluation (including one text tokenization).
	// Appended questions add their own evidence without rebuilding that base.
	basePresent := len(queries) >= len(p.baseQueries)
	for i, query := range p.baseQueries {
		if i >= len(queries) || queries[i] != query {
			basePresent = false
			break
		}
	}
	if basePresent {
		memos = append(memos, p.baseLexical)
		queries = queries[len(p.baseQueries):]
	}
	for _, query := range queries {
		memo, ok := p.lexical[query]
		if !ok {
			memo = &prepareTurnRelevanceMemo{queries: []string{query}, texts: &p.scoredTexts}
			memo.score = prepareTurnPriorityRelevanceScorer(memo.queries, "", p.lexicalText)
			p.lexical[query] = memo
		}
		memos = append(memos, memo)
	}
	return memos
}

func (p *prepareTurnRequestPreparation) relevanceScorer(queries []string, fallback string) func(string) float64 {
	memos := p.relevanceMemos(queries, fallback)
	return func(text string) float64 {
		defer p.measurement().start("assembly.relevance_score").end()
		id := p.scoredTexts.id(text)
		best := 0.0
		for _, memo := range memos {
			best = math.Max(best, memo.getID(id, text))
		}
		return best
	}
}

// primeRelevanceScores fills the memos read by relevanceScorer(queries,
// fallback) for texts, computing missing scores concurrently. Each worker
// scores with its own scorer; analyses are cached as lexicalText would.
func (p *prepareTurnRequestPreparation) primeRelevanceScores(queries []string, fallback string, texts []string) {
	if p == nil {
		return
	}
	memos := p.relevanceMemos(queries, fallback)
	p.primeLexicalTexts(texts)
	ids := make([]int32, len(texts))
	for i, text := range texts {
		ids[i] = p.scoredTexts.id(text)
	}
	queued := make([]bool, len(p.scoredTexts.ids))
	for _, memo := range memos {
		missing, missingIDs := []string{}, []int32{}
		for i, text := range texts {
			if id := ids[i]; !memo.has(id) && !queued[id] {
				queued[id] = true
				missing, missingIDs = append(missing, text), append(missingIDs, id)
			}
		}
		scores := prepareTurnParallelScores(missing, func() func(string) float64 {
			return prepareTurnPriorityRelevanceScorer(memo.queries, memo.fallback, p.cachedLexicalText)
		})
		for i, id := range missingIDs {
			memo.set(id, scores[i])
			queued[id] = false
		}
	}
}

// primeLexicalTexts analyzes uncached texts concurrently and caches them in
// first-seen order, with one tokenize call each as lexicalText records.
func (p *prepareTurnRequestPreparation) primeLexicalTexts(texts []string) {
	missing := []string{}
	queued := map[string]bool{}
	for _, text := range texts {
		if _, ok := p.lexicalTexts[text]; !ok && !queued[text] {
			queued[text] = true
			missing = append(missing, text)
		}
	}
	if len(missing) == 0 {
		return
	}
	started := time.Now()
	analyzed := make([]prepareTurnPriorityLexicalText, len(missing))
	prepareTurnParallelFor(len(missing), func(i int) {
		analyzed[i] = prepareTurnPriorityAnalyzeText(missing[i])
	})
	for i, text := range missing {
		p.lexicalTexts[text] = analyzed[i]
	}
	p.measurement().record("assembly.tokenize", len(missing), time.Since(started))
}

// cachedLexicalText reads an analysis primed by primeLexicalTexts; it never
// writes, so concurrent scorers may share it.
func (p *prepareTurnRequestPreparation) cachedLexicalText(text string) prepareTurnPriorityLexicalText {
	if value, ok := p.lexicalTexts[text]; ok {
		return value
	}
	return prepareTurnPriorityAnalyzeText(text)
}

func (p *prepareTurnRequestPreparation) publicMemorySeeds(item store.Memory) []prepareTurnPriorityFactSeed {
	if seeds, ok := p.memorySeeds[item]; ok {
		return seeds
	}
	projected, ok := publicMemoryFromCanonical(item)
	if !ok {
		p.memorySeeds[item] = nil
		return nil
	}
	facts, projection := prepareTurnPriorityFactsFromMemory(projected)
	summary := p.summary(projected)
	metadata := prepareTurnPrioritySourceMetadata{
		Lane: "event_recent", SourceTable: "memories", Tier: "required",
		LineKey: prepareTurnPriorityCleanLine(summary), SourceRowID: prepareTurnMemorySourceRowID(projected),
		SourceOccurrence: prepareTurnMemorySourceOccurrenceKey(projected), SourceTurn: projected.TurnIndex,
		Importance: prepareTurnPriorityNormalizeScore(projected.Importance), ImportancePresent: projected.Importance > 0, Visibility: "public_projection",
	}
	out := prepareTurnInjectionAssembly{}
	appendPrepareTurnPriorityFactSeeds(&out, metadata, summary, facts, projection)
	p.memorySeeds[item] = out.PriorityFactSeeds
	return out.PriorityFactSeeds
}

func prepareTurnProjectionQuerySet(selection prepareTurnMemorySelectionContext, fallback string) []string {
	if strings.TrimSpace(selection.Query) == "" {
		return []string{fallback}
	}
	return prepareTurnPriorityQuerySetFromAny(selection.QuerySet)
}

// These fragments contain source preparation only. Their order is the original
// source traversal order, so score ties and source occurrence identities do not
// change when supplemental questions reuse them. Lifetime: one prepare request.
type prepareTurnFactFragment struct {
	seeds    []prepareTurnPriorityFactSeed
	metadata []prepareTurnPrioritySourceMetadata
}

type prepareTurnFactFragmentOffset struct{ seeds, metadata int }

func prepareTurnFactFragmentStart(out *prepareTurnInjectionAssembly) prepareTurnFactFragmentOffset {
	return prepareTurnFactFragmentOffset{len(out.PriorityFactSeeds), len(out.PrioritySourceMetadata)}
}

func (start prepareTurnFactFragmentOffset) capture(out *prepareTurnInjectionAssembly) prepareTurnFactFragment {
	return prepareTurnFactFragment{out.PriorityFactSeeds[start.seeds:], out.PrioritySourceMetadata[start.metadata:]}
}

func (fragment prepareTurnFactFragment) appendTo(out *prepareTurnInjectionAssembly) {
	out.PriorityFactSeeds = append(out.PriorityFactSeeds, fragment.seeds...)
	out.PrioritySourceMetadata = append(out.PrioritySourceMetadata, fragment.metadata...)
}

func clonePrepareTurnProjectionCounts(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
