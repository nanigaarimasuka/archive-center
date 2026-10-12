package httpapi

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/risulongmemory/archive-center-go/internal/store"
)

// refMemoryRecallMatcher is prepareTurnMemoryRecallMatcher before it skipped
// encoding empty anchors and walked the smaller phrase set.
func refMemoryRecallMatcher(query string) func(prepareTurnRecallMemory) prepareTurnRecallEvidence {
	query = strings.TrimSpace(query)
	phrases := prepareTurnRecallPhrasePairs(query)
	termsByAnchors := map[string][]string{"": prepareTurnDistinctiveRecallTerms(query)}
	// The query is fixed for this matcher: normalize it once, and decide each
	// distinct anchor once, exactly as prepareTurnRecallContainsAnchor does.
	queryNeedle := normalizePrepareTurnEntityNeedle(query)
	anchorMatches := map[string]bool{}
	containsAnchor := func(anchor string) bool {
		matched, ok := anchorMatches[anchor]
		if !ok {
			matched = strings.TrimSpace(anchor) != "" && strings.Contains(queryNeedle, normalizePrepareTurnEntityNeedle(anchor))
			anchorMatches[anchor] = matched
		}
		return matched
	}
	return func(item prepareTurnRecallMemory) prepareTurnRecallEvidence {
		if query == "" {
			return prepareTurnRecallEvidence{}
		}
		evidence := prepareTurnRecallEvidence{}
		for _, anchor := range item.anchors {
			if containsAnchor(anchor) {
				evidence.StructuredAnchors = append(evidence.StructuredAnchors, anchor)
			}
		}
		encodedAnchors, _ := json.Marshal(evidence.StructuredAnchors)
		key := string(encodedAnchors)
		if len(evidence.StructuredAnchors) == 0 {
			key = ""
		}
		queryTerms, ok := termsByAnchors[key]
		if !ok {
			queryTerms = prepareTurnDistinctiveRecallTerms(query, evidence.StructuredAnchors...)
			termsByAnchors[key] = queryTerms
		}
		for _, term := range queryTerms {
			if item.terms[term] {
				evidence.OverlapTerms = append(evidence.OverlapTerms, term)
			}
		}
		for pair := range item.phrases {
			if phrases[pair] {
				evidence.ExactPhrase = true
				break
			}
		}
		required := prepareTurnDynamicOverlapRequirement(len(queryTerms))
		if item.distinctiveCount > 0 {
			required = minInt(required, (item.distinctiveCount+1)/2)
		}
		evidence.LexicalOverlap = len(evidence.OverlapTerms) >= required
		evidence.Eligible = evidence.ExactPhrase || evidence.LexicalOverlap || (item.protected && len(evidence.StructuredAnchors) > 0)
		return evidence
	}
}

func TestMemoryRecallMatcherMatchesReference(t *testing.T) {
	r := rand.New(rand.NewSource(71))
	words := []string{"Mina", "brass", "key", "old", "observatory", "vow", "앨리스", "서울역", "열쇠를", "the", "gate", "Kael", "map"}
	text := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = words[r.Intn(len(words))]
		}
		return strings.Join(parts, " ")
	}
	for c := 0; c < 300; c++ {
		query := text(r.Intn(12))
		got, want := prepareTurnMemoryRecallMatcher(query), refMemoryRecallMatcher(query)
		for i := 0; i < 20; i++ {
			summary := map[string]any{"turn_summary": text(1 + r.Intn(10))}
			if r.Intn(2) == 0 {
				summary["characters"] = []any{words[r.Intn(len(words))]}
			}
			raw, _ := json.Marshal(summary)
			item := prepareTurnPrepareRecallMemory(store.Memory{ID: int64(i), SummaryJSON: string(raw)})
			if a, b := got(item), want(item); !reflect.DeepEqual(a, b) {
				t.Fatalf("query %q item %s: %#v, want %#v", query, raw, a, b)
			}
		}
	}
	_ = fmt.Sprint
}
