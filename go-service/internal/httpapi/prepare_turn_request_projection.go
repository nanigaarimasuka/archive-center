package httpapi

import (
	"crypto/sha256"
	"encoding/json"
	"math"
	"strings"

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

// prepareTurnSourceSeedJSONDigest is the SHA-256 of the seed's JSON without
// its query discoveries, which are observations on the source, not its
// identity. prepareTurnSourceSeedDigest gives seeds the same equality.
func prepareTurnSourceSeedJSONDigest(seed prepareTurnPriorityFactSeed) [32]byte {
	seed.SourceSelectionScore = 0
	seed.SourceSelectionScoreObserved, seed.SourceSelectionScoreIsVector = false, false
	seed.RecallQueries = nil
	seed.SemanticSimilarity, seed.SemanticSimilarityObserved = 0, false
	seed.SemanticUnitID, seed.SemanticSimilaritySource = "", ""
	encoded, _ := json.Marshal(seed)
	return sha256.Sum256(encoded)
}

// json writes a loosely typed value as JSON, as the seed's JSON holds it.
func (w *prepareTurnSeedDigestWriter) json(value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		w.unusable = true
	}
	w.length(len(encoded))
	w.h.Write(encoded)
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

func (p *prepareTurnRequestPreparation) lexicalText(text string) prepareTurnPriorityLexicalText {
	if value, ok := p.lexicalTexts[text]; ok {
		return value
	}
	defer p.measurement().start("assembly.tokenize").end()
	value := prepareTurnPriorityAnalyzeText(text)
	p.lexicalTexts[text] = value
	return value
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
