package httpapi

import (
	"math"
	"math/rand"
	"strings"
	"testing"
)

// A query set scores the best of its queries scored alone, so stopping at a
// perfect score never changes the result.
func TestPriorityRelevanceIsBestSingleQueryScore(t *testing.T) {
	r := rand.New(rand.NewSource(41))
	words := []string{"앨리스", "앨리스가", "서울역", "서울역에서", "door", "doors", "red", "the", "카페", "비", "열쇠를", "열쇠", "x", "광장"}
	text := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = words[r.Intn(len(words))]
		}
		return strings.Join(parts, " ")
	}
	for c := 0; c < 3000; c++ {
		queries := make([]string, 1+r.Intn(5))
		for i := range queries {
			queries[i] = text(r.Intn(6))
		}
		candidate := text(r.Intn(10))
		want := 0.0
		for _, query := range queries {
			want = math.Max(want, prepareTurnPriorityRelevanceScorer([]string{query}, "")(candidate))
		}
		if got := prepareTurnPriorityRelevanceScorer(queries, "")(candidate); got != want {
			t.Fatalf("queries %q text %q: %v, want %v", queries, candidate, got, want)
		}
	}
}

// A text containing the whole query scores 1, whatever its term overlap.
func TestPriorityRelevanceContainedQueryScoresOne(t *testing.T) {
	r := rand.New(rand.NewSource(43))
	words := []string{"앨리스", "서울역", "door", "red", "카페", "비", "열쇠", "광장", "the", "old"}
	for c := 0; c < 2000; c++ {
		parts := make([]string, 2+r.Intn(4))
		for i := range parts {
			parts[i] = words[r.Intn(len(words))]
		}
		query := strings.Join(parts, " ")
		candidate := strings.Join([]string{words[r.Intn(len(words))], query, words[r.Intn(len(words))]}, " ")
		if len(prepareTurnRecallTerms(query)) == 0 {
			continue
		}
		if got := prepareTurnPriorityRelevanceScorer([]string{query}, "")(candidate); got != 1 {
			t.Fatalf("query %q in %q scored %v", query, candidate, got)
		}
	}
}

// Here term overlap alone gives 0.5 ("door" is not "doo"); the contained
// query still makes it 1.
func TestPriorityRelevanceContainedQueryRaisesPartialOverlap(t *testing.T) {
	if got := prepareTurnPriorityRelevanceScorer([]string{"red doo"}, "")("red door"); got != 1 {
		t.Fatalf("score %v, want 1", got)
	}
}
