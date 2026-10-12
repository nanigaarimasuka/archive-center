package httpapi

import (
	"math/rand"
	"strings"
	"testing"
)

// refSupportRecallEligible is prepareTurnSupportRecallEligible before the
// query side was cached.
func refSupportRecallEligible(query, text string, anchors ...string) bool {
	query = strings.TrimSpace(query)
	text = strings.TrimSpace(text)
	if query == "" || text == "" {
		return query == "" && text != ""
	}
	if prepareTurnSharedRecallPhrase(query, text) {
		return true
	}
	queryTerms := prepareTurnDistinctiveRecallTerms(query, anchors...)
	overlap := prepareTurnDistinctiveRecallOverlapCount(queryTerms, text)
	return overlap >= prepareTurnRecallRequiredOverlap(queryTerms, text)
}

func TestSupportRecallEligibleMatchesUncached(t *testing.T) {
	r := rand.New(rand.NewSource(29))
	words := []string{"", "Alice", "alice", "앨리스가", "앨리스", "서울역", "서울역에서", "카페", "the", "old", "red", "door", "문을", "열었다", "열다", "학생이다", "x", "y", "광장", "비", "go"}
	text := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = words[r.Intn(len(words))]
		}
		return strings.Join(parts, []string{" ", ", ", "\n"}[r.Intn(3)])
	}
	queries := make([]string, 50) // more than the cache holds, so entries are evicted and rebuilt
	for i := range queries {
		queries[i] = text(r.Intn(10))
	}
	for c := 0; c < 6000; c++ {
		query := queries[r.Intn(len(queries))]
		var anchors []string
		for i := r.Intn(3); i > 0; i-- {
			anchors = append(anchors, text(1+r.Intn(2)))
		}
		candidate := text(r.Intn(8))
		if got, want := prepareTurnSupportRecallEligible(query, candidate, anchors...), refSupportRecallEligible(query, candidate, anchors...); got != want {
			t.Fatalf("query %q text %q anchors %q: %v, want %v", query, candidate, anchors, got, want)
		}
	}
}
