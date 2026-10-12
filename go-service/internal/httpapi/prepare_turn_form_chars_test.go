package httpapi

import (
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMemoryFormCharsMatchesRuneCount(t *testing.T) {
	r := rand.New(rand.NewSource(47))
	pieces := []string{"", " ", "  ", "\t", "\n", "가", "a", "x y", " ", "\u0085", "tail ", " lead", "\xe2\x82", "\xac", "\xff", "상태: 맑음", "　"}
	piece := func() string {
		var b strings.Builder
		for n := r.Intn(3); n > 0; n-- {
			b.WriteString(pieces[r.Intn(len(pieces))])
		}
		return b.String()
	}
	for c := 0; c < 20000; c++ {
		heading := strings.TrimSpace(piece())
		parts := make([]prepareTurnMemoryFormPart, r.Intn(4))
		lines := make([]string, len(parts))
		for i := range parts {
			parts[i].Text = piece()
			lines[i] = "  " + parts[i].Text
		}
		text := strings.TrimSpace(heading + "\n" + strings.Join(lines, "\n"))
		if got, want := prepareTurnMemoryFormChars(heading, parts, text, measurePrepareTurnText), utf8.RuneCountInString(text); got != want {
			t.Fatalf("heading %q parts %q: %d, want %d", heading, lines, got, want)
		}
	}
}
