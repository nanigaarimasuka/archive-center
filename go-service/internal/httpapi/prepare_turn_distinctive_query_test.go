package httpapi

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

func TestDistinctiveQueryMatchesDistinctiveRecallTerms(t *testing.T) {
	r := rand.New(rand.NewSource(73))
	words := []string{"", "a", "Mina", "mina", "brass", "key", "keys", "the", "앨리스가", "앨리스", "서울역에서", "서울역", "x-y", "_z", "열쇠를", "열쇠", "old", "vow", "학생이다", "go"}
	text := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = words[r.Intn(len(words))]
		}
		return strings.Join(parts, []string{" ", ", ", "\n"}[r.Intn(3)])
	}
	for c := 0; c < 2000; c++ {
		query := text(r.Intn(12))
		prepared := newPrepareTurnDistinctiveQuery(query)
		for i := 0; i < 5; i++ {
			anchors := []string{}
			for n := r.Intn(3); n > 0; n-- {
				anchors = append(anchors, text(1+r.Intn(2)))
			}
			if got, want := prepared.terms(anchors...), prepareTurnDistinctiveRecallTerms(query, anchors...); !reflect.DeepEqual(got, want) {
				t.Fatalf("query %q anchors %q: %q, want %q", query, anchors, got, want)
			}
		}
	}
}
