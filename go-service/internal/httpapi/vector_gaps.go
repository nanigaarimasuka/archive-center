package httpapi

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/risulongmemory/archive-center-go/internal/store"
	"github.com/risulongmemory/archive-center-go/internal/vector"
)

// adminVectorGaps compares the stored vector documents with the rows that
// should have one. A reindex that is not forced then embeds only the rows
// without a vector and removes the vectors without a row, instead of
// embedding the whole session again because a few documents differ.
type adminVectorGaps struct {
	MissingTurns        map[int]bool
	MissingRevisions    map[string]bool
	MissingWorldRuleIDs map[int64]bool
	OrphanIDs           []string
	MissingCount        int
}

func (g adminVectorGaps) empty() bool {
	return g.MissingCount == 0 && len(g.OrphanIDs) == 0
}

func (s *Server) adminVectorGaps(ctx context.Context, sid string) (adminVectorGaps, error) {
	gaps := adminVectorGaps{
		MissingTurns:        map[int]bool{},
		MissingRevisions:    map[string]bool{},
		MissingWorldRuleIDs: map[int64]bool{},
	}
	if s == nil || s.Store == nil || s.Vector == nil {
		return gaps, store.ErrNotEnabled
	}
	lister, ok := s.Vector.(vector.DocumentLister)
	if !ok {
		return gaps, store.ErrNotEnabled
	}
	docs, err := lister.ListDocuments(ctx, sid)
	if err != nil {
		return gaps, err
	}
	present := map[string]bool{}
	for _, doc := range docs {
		present[strings.TrimSpace(doc.ID)] = true
	}
	expected := map[string]bool{}
	// has records the ids a row may be stored under and reports whether any
	// of them is present.
	has := func(ids ...string) bool {
		found := false
		for _, id := range ids {
			if id = strings.TrimSpace(id); id == "" {
				continue
			}
			expected[id] = true
			if present[id] {
				found = true
			}
		}
		return found
	}
	ignorable := func(err error) bool { return err == nil || errors.Is(err, store.ErrNotEnabled) }

	memories, err := s.Store.ListMemories(ctx, sid, 0, 0)
	if !ignorable(err) {
		return gaps, err
	}
	for _, item := range memories {
		if item.ID <= 0 || strings.TrimSpace(reindexMemoryDocumentText(item)) == "" {
			continue
		}
		// A row without an embedding is replayed like a row without a vector.
		hasVector := has(memoryVectorDocumentID(sid, item), rollbackVectorDocumentAlias("memory", sid, item.ID), rollbackVectorDocumentLegacyAlias("memory", item.ID))
		if !hasVector || len(parseFloat32JSONList(strings.TrimSpace(item.Embedding))) == 0 {
			gaps.MissingTurns[item.TurnIndex] = true
			gaps.MissingCount++
		}
	}
	evidence, err := s.Store.ListEvidence(ctx, sid)
	if !ignorable(err) {
		return gaps, err
	}
	for _, item := range evidence {
		if item.ID <= 0 || !adminEvidenceVectorEligible(item) {
			continue
		}
		if !has(derivedArtifactVectorDocumentID("evidence", sid, item.ID), rollbackVectorDocumentAlias("evidence", sid, item.ID), rollbackVectorDocumentLegacyAlias("evidence", item.ID)) {
			turn := item.TurnAnchor
			if turn == 0 {
				turn = item.SourceTurnEnd
			}
			if turn == 0 {
				turn = item.SourceTurnStart
			}
			gaps.MissingTurns[turn] = true
			gaps.MissingCount++
		}
	}
	if reader, ok := s.Store.(store.GeneralVectorPreciseMemoryReader); ok {
		units, err := reader.ListGeneralVectorPreciseMemoryUnits(ctx, sid)
		if !ignorable(err) {
			return gaps, err
		}
		for _, item := range units {
			unitID := strings.TrimSpace(item.UnitID)
			if unitID == "" || !store.PreciseMemoryGeneralVectorEligible(&item) {
				continue
			}
			if !has("precise_memory:" + sid + ":" + unitID) {
				gaps.MissingRevisions[strings.TrimSpace(item.SourceRevision)] = true
				gaps.MissingTurns[item.SourceTurnEnd] = true
				gaps.MissingCount++
			}
		}
	}
	worldRules, err := s.Store.ListWorldRules(ctx, sid)
	if !ignorable(err) {
		return gaps, err
	}
	for _, item := range worldRules {
		if item.ID <= 0 || !adminWorldRuleVectorEligible(item) {
			continue
		}
		if !has(derivedArtifactVectorDocumentID("world_rule", sid, item.ID), rollbackVectorDocumentAlias("world_rule", sid, item.ID), rollbackVectorDocumentLegacyAlias("world_rule", item.ID)) {
			gaps.MissingWorldRuleIDs[item.ID] = true
			gaps.MissingCount++
		}
	}
	for _, doc := range docs {
		if adminManagedVectorTier(doc) == "" {
			continue
		}
		if id := strings.TrimSpace(doc.ID); id != "" && !expected[id] {
			gaps.OrphanIDs = append(gaps.OrphanIDs, id)
		}
	}
	return gaps, nil
}

// onlyGapCandidates keeps the canonical replays that own a missing vector.
func (g adminVectorGaps) onlyGapCandidates(candidates []adminCanonicalMemoryReplayCandidate) []adminCanonicalMemoryReplayCandidate {
	out := []adminCanonicalMemoryReplayCandidate{}
	for _, candidate := range candidates {
		if candidate.Source == nil {
			continue
		}
		if g.MissingTurns[candidate.Source.TurnIndex] || g.MissingRevisions[strings.TrimSpace(candidate.Source.SourceRevision)] {
			out = append(out, candidate)
		}
	}
	return out
}

func (g adminVectorGaps) onlyGapWorldRules(rules []store.WorldRule) []store.WorldRule {
	out := []store.WorldRule{}
	for _, rule := range rules {
		if g.MissingWorldRuleIDs[rule.ID] {
			out = append(out, rule)
		}
	}
	return out
}

func (g adminVectorGaps) preview() map[string]any {
	turns := []int{}
	for turn := range g.MissingTurns {
		turns = append(turns, turn)
	}
	orphans := g.OrphanIDs
	if len(orphans) > 100 {
		orphans = orphans[:100]
	}
	return map[string]any{
		"missing_count":       g.MissingCount,
		"missing_turns":       uniqueSortedInts(turns),
		"missing_world_rules": len(g.MissingWorldRuleIDs),
		"orphan_count":        len(g.OrphanIDs),
		"orphan_ids":          orphans,
	}
}

// openRouterModelVariants are the suffixes OpenRouter accepts after a model
// id to pick how it is served (":floor" is the cheapest provider). They do
// not change the model, and its responses name the model without them.
var openRouterModelVariants = map[string]bool{
	"free": true, "extended": true, "exacto": true, "thinking": true,
	"online": true, "nitro": true, "floor": true, "beta": true,
}

// reembedNeedsWholeSession reports whether a re-embed reason of the integrity
// report means every vector must be embedded again. Memory rows without an
// embedding do not: the gap repair replays only their turns.
func reembedNeedsWholeSession(reasons []string) bool {
	for _, reason := range reasons {
		if reason != "memory_rows_missing_embedding" {
			return true
		}
	}
	return false
}

// sameEmbeddingModel reports whether two embedding model names are the same
// model: case is ignored, and so is an OpenRouter variant suffix. Other tags
// after ":" (an Ollama "qwen3-embedding:8b") still tell models apart.
func sameEmbeddingModel(a, b string) bool {
	return embeddingModelKey(a) == embeddingModelKey(b)
}

// embeddingModelKeys caches each name's key: names are few and are compared
// once per stored row.
var (
	embeddingModelKeysMu sync.RWMutex
	embeddingModelKeys   = map[string]string{}
)

// embeddingModelKey is the name every variant of a model comes down to:
// "Qwen/Qwen3-Embedding-8B:nitro" -> "qwen/qwen3-embedding-8b".
func embeddingModelKey(model string) string {
	embeddingModelKeysMu.RLock()
	key, ok := embeddingModelKeys[model]
	embeddingModelKeysMu.RUnlock()
	if ok {
		return key
	}
	key = strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(key, ":"); i > 0 && openRouterModelVariants[key[i+1:]] {
		key = key[:i]
	}
	embeddingModelKeysMu.Lock()
	embeddingModelKeys[model] = key
	embeddingModelKeysMu.Unlock()
	return key
}
