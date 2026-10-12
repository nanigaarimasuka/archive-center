package httpapi

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
)

// The index must find exactly the tokens the per-token check matched:
// strings.EqualFold or prepareTurnPriorityInflectedNonASCIIMatch.
func TestStateMentionIndexMatchesPerTokenCheck(t *testing.T) {
	r := rand.New(rand.NewSource(23))
	pieces := []string{"미라", "오렌", "라", "가", "는", "의", "을", "Mira", "MIRA", "mira", "K", "K", "k", "é", "É", "x", " ", "\xff", "\xfe", "�", "a"}
	word := func() string {
		var b strings.Builder
		for i := 0; i < 1+r.Intn(3); i++ {
			b.WriteString(pieces[r.Intn(len(pieces))])
		}
		return b.String()
	}
	for c := 0; c < 2000; c++ {
		sourceTerms := make([][]string, 1+r.Intn(6))
		for i := range sourceTerms {
			for j := 0; j < r.Intn(8); j++ {
				sourceTerms[i] = append(sourceTerms[i], word())
			}
		}
		index := newPrepareTurnStateMentionIndex(sourceTerms)
		for q := 0; q < 5; q++ {
			subject := word()
			if r.Intn(3) == 0 && len(index.tokens) > 0 {
				subject = index.tokens[r.Intn(len(index.tokens))] + pieces[r.Intn(len(pieces))]
			}
			var want []int
			for id, token := range index.tokens {
				if strings.EqualFold(token, subject) || prepareTurnPriorityInflectedNonASCIIMatch(token, subject) {
					want = append(want, id)
				}
			}
			got := index.matchingTokens(subject)
			sort.Ints(got)
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("case %d subject %q tokens %q: got %v, want %v", c, subject, index.tokens, got, want)
			}
		}
		// Postings list each fact once, in fact order.
		for id, facts := range index.factsByToken {
			for k := 1; k < len(facts); k++ {
				if facts[k] <= facts[k-1] {
					t.Fatalf("token %q postings not ascending: %v", index.tokens[id], facts)
				}
			}
		}
	}
}
