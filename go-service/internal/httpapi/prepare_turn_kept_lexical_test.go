package httpapi

import (
	"math/rand"
	"strings"
	"testing"
)

func newKeptLexicalTestPreparation() *prepareTurnRequestPreparation {
	return &prepareTurnRequestPreparation{
		lexicalTexts: map[string]prepareTurnPriorityLexicalText{},
		lexParts:     map[string]*prepareTurnLexicalPart{},
		lexFields:    map[string]*prepareTurnLexicalField{},
		lexIDs:       map[string]int32{},
		partNeedles:  map[string]prepareTurnNeedleSegment{},
	}
}

// Meanings analyzed by an earlier request and kept are given the later
// request's ids; one scorer shared across all readings, kept or not, must
// still score each exactly as its joined text.
func TestKeptLexicalAnalysisScoresAsJoinedText(t *testing.T) {
	r := rand.New(rand.NewSource(29))
	alphabet := []string{"a", "b", "ab", " ", "가", "나", "다", "의", ".", "x", "y", "Mira", "oren"}
	word := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(alphabet[r.Intn(len(alphabet))])
		}
		return b.String()
	}
	for c := 0; c < 300; c++ {
		pool := make([]string, 6+r.Intn(10))
		for i := range pool {
			pool[i] = word(r.Intn(10))
		}
		type reading struct {
			parts  []string
			joined string
			kept   *prepareTurnKeptLexical
		}
		readings := make([]reading, 4+r.Intn(10))
		for i := range readings {
			parts := make([]string, 1+r.Intn(6))
			for j := range parts {
				parts[j] = pool[r.Intn(len(pool))]
			}
			readings[i] = reading{parts: parts, joined: strings.Join(parts, "\n"), kept: &prepareTurnKeptLexical{}}
		}
		items := func(from int) []prepareTurnJoinedLexicalText {
			out := []prepareTurnJoinedLexicalText{}
			for _, reading := range readings[from:] {
				// Parts are new strings in each request.
				parts := make([]string, len(reading.parts))
				for j, part := range reading.parts {
					parts[j] = strings.Clone(part)
				}
				out = append(out, prepareTurnJoinedLexicalText{joined: reading.joined, parts: parts, kept: reading.kept})
			}
			return out
		}
		// The first request keeps the analyses of some readings.
		first := newKeptLexicalTestPreparation()
		first.primeJoinedLexicalTexts(items(len(readings) / 2))
		// The second analyzes every reading, reusing the kept ones.
		second := newKeptLexicalTestPreparation()
		second.primeJoinedLexicalTexts(items(0))
		// A meaning repeated by another reading is analyzed, and kept, once.
		keptMeanings := map[string]bool{}
		for _, reading := range readings {
			keptMeanings[reading.joined] = keptMeanings[reading.joined] || reading.kept.data.Load() != nil
		}
		for joined, kept := range keptMeanings {
			if !kept {
				t.Fatalf("case %d: analysis of %q not kept", c, joined)
			}
		}
		for q := 0; q < 3; q++ {
			query := word(1 + r.Intn(6))
			if source := readings[r.Intn(len(readings))].joined; len(source) > 3 && q == 2 {
				start := r.Intn(len(source) - 1)
				query = strings.ToValidUTF8(source[start:start+1+r.Intn(len(source)-start-1)], "")
			}
			cached := prepareTurnPriorityRelevanceScorer([]string{query}, "", second.cachedLexicalText)
			whole := prepareTurnPriorityRelevanceScorer([]string{query}, "")
			for pass := 0; pass < 2; pass++ {
				for _, reading := range readings {
					if got, want := cached(reading.joined), whole(reading.joined); got != want {
						t.Fatalf("case %d query %q parts %q: kept %v, joined %v", c, query, reading.parts, got, want)
					}
				}
			}
		}
		// Each request's ids are its own: a term's id is its value's id.
		for _, reading := range readings {
			analyzed := second.lexicalTexts[reading.joined]
			for k, term := range analyzed.terms {
				if analyzed.termIDs[k] != second.lexIDs[term.value] {
					t.Fatalf("case %d: term %q has id %d, want %d", c, term.value, analyzed.termIDs[k], second.lexIDs[term.value])
				}
			}
		}
	}
}
