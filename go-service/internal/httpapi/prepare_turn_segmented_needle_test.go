package httpapi

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// Readings are scored from cached, segmented parts. Matches that span part
// boundaries, including parts shorter than the query, must score exactly as
// the joined text does.
func TestSegmentedNeedleScoreEqualsJoinedText(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	alphabet := []string{"a", "b", "ab", " ", "가", "나", "다", "의", ".", "x", "y"}
	word := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(alphabet[r.Intn(len(alphabet))])
		}
		return b.String()
	}
	for c := 0; c < 3000; c++ {
		p := &prepareTurnRequestPreparation{
			lexicalTexts: map[string]prepareTurnPriorityLexicalText{},
			lexParts:     map[string]*prepareTurnLexicalPart{},
			lexFields:    map[string]*prepareTurnLexicalField{},
			lexIDs:       map[string]int32{},
			partNeedles:  map[string]prepareTurnNeedleSegment{},
		}
		parts := make([]string, 2+r.Intn(6))
		for i := range parts {
			parts[i] = word(r.Intn(12))
		}
		joined := strings.Join(parts, "\n")
		p.primeJoinedLexicalTexts([]prepareTurnJoinedLexicalText{{joined: joined, parts: parts}})
		queries := []string{word(1 + r.Intn(8))}
		if len(joined) > 4 {
			start := r.Intn(len(joined) - 2)
			end := start + 1 + r.Intn(len(joined)-start-1)
			queries = append(queries, strings.ToValidUTF8(joined[start:end], ""))
		}
		for _, query := range queries {
			segmented := prepareTurnPriorityRelevanceScorer([]string{query}, "", p.cachedLexicalText)
			whole := prepareTurnPriorityRelevanceScorer([]string{query}, "")
			// Score twice so cached segment hits are exercised too.
			for pass := 0; pass < 2; pass++ {
				if got, want := segmented(joined), whole(joined); got != want {
					t.Fatalf("case %d query %q parts %q: segmented %v, joined %v", c, query, parts, got, want)
				}
			}
		}
	}
}

// One scorer reused across many readings built from a shared pool of parts
// reuses its per-segment and per-boundary results; scores must still equal
// scoring each joined text on its own.
func TestSegmentedNeedleScoreAcrossSharedReadings(t *testing.T) {
	r := rand.New(rand.NewSource(13))
	alphabet := []string{"a", "b", "ab", " ", "가", "나", "다", "의", ".", "x"}
	word := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(alphabet[r.Intn(len(alphabet))])
		}
		return b.String()
	}
	for c := 0; c < 300; c++ {
		p := &prepareTurnRequestPreparation{
			lexicalTexts: map[string]prepareTurnPriorityLexicalText{},
			lexParts:     map[string]*prepareTurnLexicalPart{},
			lexFields:    map[string]*prepareTurnLexicalField{},
			lexIDs:       map[string]int32{},
			partNeedles:  map[string]prepareTurnNeedleSegment{},
		}
		pool := make([]string, 6)
		for i := range pool {
			pool[i] = word(r.Intn(10))
		}
		var readings []prepareTurnJoinedLexicalText
		for i := 0; i < 20; i++ {
			parts := []string{word(r.Intn(6))}
			for j := 0; j < 2+r.Intn(5); j++ {
				parts = append(parts, pool[r.Intn(len(pool))])
			}
			parts = append(parts, word(r.Intn(6)))
			readings = append(readings, prepareTurnJoinedLexicalText{joined: strings.Join(parts, "\n"), parts: parts})
		}
		p.primeJoinedLexicalTexts(readings)
		query := word(1 + r.Intn(9))
		if r.Intn(2) == 0 {
			joined := readings[r.Intn(len(readings))].joined
			if len(joined) > 3 {
				start := r.Intn(len(joined) - 2)
				query = strings.ToValidUTF8(joined[start:start+1+r.Intn(len(joined)-start-1)], "")
			}
		}
		segmented := prepareTurnPriorityRelevanceScorer([]string{query}, "", p.cachedLexicalText)
		for _, reading := range readings {
			whole := prepareTurnPriorityRelevanceScorer([]string{query}, "")
			if got, want := segmented(reading.joined), whole(reading.joined); got != want {
				t.Fatalf("case %d query %q parts %q: segmented %v, joined %v", c, query, reading.parts, got, want)
			}
		}
	}
}

// Needles longer than the screening prefix: found, absent, and sharing only
// their first bytes with the text.
func TestSegmentedNeedleScoreForLongNeedles(t *testing.T) {
	r := rand.New(rand.NewSource(17))
	alphabet := []string{"a", "b", "가", "나", "다", "x", "y", "z", "q"}
	word := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(alphabet[r.Intn(len(alphabet))])
		}
		return b.String()
	}
	for c := 0; c < 500; c++ {
		p := &prepareTurnRequestPreparation{
			lexicalTexts: map[string]prepareTurnPriorityLexicalText{},
			lexParts:     map[string]*prepareTurnLexicalPart{},
			lexFields:    map[string]*prepareTurnLexicalField{},
			lexIDs:       map[string]int32{},
			partNeedles:  map[string]prepareTurnNeedleSegment{},
		}
		pool := make([]string, 5)
		for i := range pool {
			pool[i] = word(3 + r.Intn(25))
		}
		var readings []prepareTurnJoinedLexicalText
		for i := 0; i < 10; i++ {
			parts := []string{word(r.Intn(10))}
			for j := 0; j < 3+r.Intn(6); j++ {
				parts = append(parts, pool[r.Intn(len(pool))])
			}
			parts = append(parts, word(r.Intn(10)))
			readings = append(readings, prepareTurnJoinedLexicalText{joined: strings.Join(parts, " "), parts: parts})
		}
		p.primeJoinedLexicalTexts(readings)
		source := readings[r.Intn(len(readings))].joined
		query := word(20 + r.Intn(40))
		if len(source) > 60 {
			start := r.Intn(len(source) - 50)
			query = strings.ToValidUTF8(source[start:start+35+r.Intn(len(source)-start-35)], "")
			if r.Intn(2) == 0 {
				query += "q" // same prefix, absent in full
			}
		}
		segmented := prepareTurnPriorityRelevanceScorer([]string{query}, "", p.cachedLexicalText)
		for _, reading := range readings {
			whole := prepareTurnPriorityRelevanceScorer([]string{query}, "")
			if got, want := segmented(reading.joined), whole(reading.joined); got != want {
				t.Fatalf("case %d query %q parts %q: segmented %v, joined %v", c, query, reading.parts, got, want)
			}
		}
	}
}

// Composed term lists must equal analyzing each joined text directly, in
// order, including readings primed in separate batches that share parts.
func TestComposedLexicalTermsEqualJoinedAnalysis(t *testing.T) {
	r := rand.New(rand.NewSource(19))
	alphabet := []string{"a", "b", "ab", " ", "가", "나", "다", "의", "는", ".", "x", "-", "s", "ing", "Ed", "들"}
	word := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(alphabet[r.Intn(len(alphabet))])
		}
		return b.String()
	}
	for c := 0; c < 200; c++ {
		p := &prepareTurnRequestPreparation{
			lexicalTexts: map[string]prepareTurnPriorityLexicalText{},
			lexParts:     map[string]*prepareTurnLexicalPart{},
			lexFields:    map[string]*prepareTurnLexicalField{},
			lexIDs:       map[string]int32{},
			partNeedles:  map[string]prepareTurnNeedleSegment{},
		}
		pool := make([]string, 8)
		for i := range pool {
			pool[i] = word(r.Intn(20))
		}
		for batch := 0; batch < 3; batch++ {
			var readings []prepareTurnJoinedLexicalText
			for i := 0; i < 15; i++ {
				parts := []string{word(r.Intn(8))}
				for j := 0; j < r.Intn(6); j++ {
					parts = append(parts, pool[r.Intn(len(pool))])
				}
				readings = append(readings, prepareTurnJoinedLexicalText{joined: strings.Join(parts, "\n"), parts: parts})
			}
			p.primeJoinedLexicalTexts(readings)
			for _, reading := range readings {
				got := p.lexicalTexts[reading.joined].terms
				want := prepareTurnPriorityAnalyzeText(reading.joined).terms
				if len(got) == 0 && len(want) == 0 {
					continue
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("case %d batch %d parts %q:\n got %v\nwant %v", c, batch, reading.parts, got, want)
				}
			}
		}
	}
}
