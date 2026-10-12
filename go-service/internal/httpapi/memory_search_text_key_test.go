package httpapi

import (
	"math/rand"
	"strings"
	"testing"
)

func TestMemorySearchTextKeyEqualsMatchesNormalizedComparison(t *testing.T) {
	r := rand.New(rand.NewSource(29))
	pieces := []string{"a", "B", "가", "나", " ", "  ", "\t", "\n", " ", "　", "\u0085", "İ", "i", "K", "K", "k", "Σ", "σ", "ς", "\xff", "\xc3", "�", "É", "é", "x", "Y"}
	gen := func() string {
		var b strings.Builder
		for i := 0; i < r.Intn(8); i++ {
			b.WriteString(pieces[r.Intn(len(pieces))])
		}
		return b.String()
	}
	normalize := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
	for c := 0; c < 200000; c++ {
		text := gen()
		key := normalize(gen())
		if r.Intn(3) == 0 {
			key = normalize(text) // force equal cases often
		}
		if got, want := memorySearchTextKeyEquals(text, key), normalize(text) == key; got != want {
			t.Fatalf("text %q key %q: got %v, want %v", text, key, got, want)
		}
	}
}
