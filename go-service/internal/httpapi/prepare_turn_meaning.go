package httpapi

import (
	"hash/maphash"
	"runtime"
	"strings"
	"sync"
	"weak"
)

// A reading form's meaning is its part values joined by "\n". Readings repeat
// the same linked state across thousands of forms, so keeping each joined
// meaning took most of a large request's memory. A form keeps the parts and a
// short token in their place: every text reader that may be given a meaning
// (the lexical analyzer and the request caches keyed by text) resolves the
// token to the joined text, so scores and analyses are those of the text.
//
// The token is derived from the joined text (a 128-bit hash, computed without
// joining), so equal meanings get one token and share one meaning.
type prepareTurnMeaning struct {
	parts []string
	token string
}

// prepareTurnMeaningTokenPrefix starts every token. A real reading text never
// starts with NUL; a token is resolved only while its meaning is alive.
const prepareTurnMeaningTokenPrefix = "\x00meaning\x00"

var prepareTurnMeanings = struct {
	seeds [2]maphash.Seed
	mu    sync.Mutex
	byKey map[string]weak.Pointer[prepareTurnMeaning]
}{
	seeds: [2]maphash.Seed{maphash.MakeSeed(), maphash.MakeSeed()},
	byKey: map[string]weak.Pointer[prepareTurnMeaning]{},
}

// prepareTurnMeaningToken is the token of parts joined by "\n".
func prepareTurnMeaningToken(parts []string) string {
	var sums [16]byte
	for i, seed := range prepareTurnMeanings.seeds {
		var h maphash.Hash
		h.SetSeed(seed)
		for j, part := range parts {
			if j > 0 {
				h.WriteByte('\n')
			}
			h.WriteString(part)
		}
		sum := h.Sum64()
		for k := 0; k < 8; k++ {
			sums[i*8+k] = byte(sum >> (8 * k))
		}
	}
	return prepareTurnMeaningTokenPrefix + string(sums[:])
}

// newPrepareTurnMeaning returns the meaning of parts joined by "\n", shared
// with any live meaning of the same text. The caller keeps it alive for as
// long as its token may be read (a form holds it).
func newPrepareTurnMeaning(parts []string) *prepareTurnMeaning {
	token := prepareTurnMeaningToken(parts)
	prepareTurnMeanings.mu.Lock()
	defer prepareTurnMeanings.mu.Unlock()
	if live := prepareTurnMeanings.byKey[token].Value(); live != nil {
		return live
	}
	m := &prepareTurnMeaning{parts: parts, token: token}
	pointer := weak.Make(m)
	prepareTurnMeanings.byKey[token] = pointer
	runtime.AddCleanup(m, func(token string) {
		prepareTurnMeanings.mu.Lock()
		if prepareTurnMeanings.byKey[token] == pointer {
			delete(prepareTurnMeanings.byKey, token)
		}
		prepareTurnMeanings.mu.Unlock()
	}, token)
	return m
}

// prepareTurnMeaningOf returns the meaning a token stands for.
func prepareTurnMeaningOf(text string) (*prepareTurnMeaning, bool) {
	if len(text) != len(prepareTurnMeaningTokenPrefix)+16 || !strings.HasPrefix(text, prepareTurnMeaningTokenPrefix) {
		return nil, false
	}
	prepareTurnMeanings.mu.Lock()
	m := prepareTurnMeanings.byKey[text].Value()
	prepareTurnMeanings.mu.Unlock()
	return m, m != nil
}

// prepareTurnMeaningText is text, or the joined meaning a token stands for.
func prepareTurnMeaningText(text string) string {
	if m, ok := prepareTurnMeaningOf(text); ok {
		return strings.Join(m.parts, "\n")
	}
	return text
}
