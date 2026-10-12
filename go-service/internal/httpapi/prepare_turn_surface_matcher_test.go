package httpapi

import (
	"math/rand"
	"strings"
	"testing"
)

func TestPrioritySurfaceMatcherMatchesSurfaceMatches(t *testing.T) {
	r := rand.New(rand.NewSource(23))
	words := []string{"", " ", "Alice", "alice", "앨리스", "앨리스가", "앨리스는", "서울", "서울에서", "the", "Bob's", "밥", "rain", "비가", "x", "광장", "카페", "A.", "\t", "Ünïcode", "ünïcode", "역", "역에"}
	text := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = words[r.Intn(len(words))]
		}
		return strings.Join(parts, []string{" ", "", ", ", "\n"}[r.Intn(4)])
	}
	for c := 0; c < 400; c++ {
		query := text(r.Intn(12))
		match := prepareTurnPrioritySurfaceMatcher(query)
		for i := 0; i < 30; i++ {
			surface := text(r.Intn(4))
			if got, want := match(surface), prepareTurnPrioritySurfaceMatches(query, surface); got != want {
				t.Fatalf("query %q surface %q: %v, want %v", query, surface, got, want)
			}
		}
	}
}
