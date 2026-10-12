package httpapi

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// A set builds the same list as repeated appendUniqueMemorySearchText calls.
func TestMemorySearchTextSetMatchesAppendUnique(t *testing.T) {
	r := rand.New(rand.NewSource(61))
	words := []string{"memories:1", "Memories:1", "memories:2", " a  b ", "a b", "A B", "Ä", "ä", "\xff", "�", "x\ty", "x y", "", "  ", "İ", "i̇"}
	for c := 0; c < 3000; c++ {
		var initial []string
		if r.Intn(3) > 0 {
			initial = []string{}
			for n := r.Intn(4); n > 0; n-- {
				initial = append(initial, words[r.Intn(len(words))])
			}
		}
		want := append([]string(nil), initial...)
		if initial != nil && len(initial) == 0 {
			want = []string{}
		}
		set := newMemorySearchTextSet(append([]string(nil), initial...))
		if initial != nil && len(initial) == 0 {
			set = newMemorySearchTextSet([]string{})
		}
		for n := r.Intn(12); n > 0; n-- {
			value := words[r.Intn(len(words))]
			if r.Intn(4) == 0 {
				value = strings.ToUpper(value) + " "
			}
			want = appendUniqueMemorySearchText(want, value)
			set.add(value)
		}
		if !reflect.DeepEqual(set.items, want) {
			t.Fatalf("case %d: %q, want %q", c, set.items, want)
		}
	}
}
