package httpapi

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// Ids follow text contents in first-seen order, whatever the storage.
func TestTextIDsFollowContents(t *testing.T) {
	r := rand.New(rand.NewSource(59))
	ids := prepareTurnTextIDs{ids: map[string]int32{}}
	want := map[string]int32{}
	pool := []string{}
	for i := 0; i < 200; i++ {
		pool = append(pool, fmt.Sprintf("text-%03d", i)) // equal lengths
	}
	for c := 0; c < 5000; c++ {
		text := pool[r.Intn(len(pool))]
		if r.Intn(2) == 0 {
			text = strings.Clone(text) // same contents, new storage
		}
		expected, ok := want[text]
		if !ok {
			expected = int32(len(want))
			want[text] = expected
		}
		if got := ids.id(text); got != expected {
			t.Fatalf("id(%q) = %d, want %d", text, got, expected)
		}
	}
}
