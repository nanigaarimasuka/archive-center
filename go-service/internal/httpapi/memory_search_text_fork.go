package httpapi

import (
	"github.com/maypok86/otter/v2"
	"github.com/risulongmemory/archive-center-go/internal/store"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The public projection of a memory depends only on its summary and evidence
// texts. Requests project the same memories along several paths and turn
// after turn, so the result is kept by those texts, bounded in size.
type publicMemoryProjectionKey struct {
	summary, evidence string
}

type publicMemoryProjected struct {
	summary, evidence string
	eligible          bool
	// The projection's search text and alias count, as
	// memorySearchTextFromMemory reports them.
	searchText string
	aliasCount int
}

const publicMemoryProjectionCacheMaxBytes = 32 << 20

var publicMemoryProjectionCache = otter.Must(&otter.Options[publicMemoryProjectionKey, publicMemoryProjected]{
	MaximumWeight: publicMemoryProjectionCacheMaxBytes,
	Weigher: func(key publicMemoryProjectionKey, projected publicMemoryProjected) uint32 {
		size := len(key.summary) + len(key.evidence) + len(projected.summary) + len(projected.evidence) + len(projected.searchText) + 64
		return uint32(min(size, math.MaxUint32))
	},
})

func cachedPublicMemoryProjection(summaryJSON, evidence string) (publicMemoryProjected, bool) {
	key := publicMemoryProjectionKey{summaryJSON, evidence}
	if projected, found := publicMemoryProjectionCache.GetIfPresent(key); found {
		return projected, projected.eligible
	}
	projection := buildPublicMemoryProjection(parseJSONMap(summaryJSON), evidence)
	projected := publicMemoryProjected{searchText: projection.SearchText.Text, aliasCount: projection.SearchText.AliasCount}
	if projection.Eligible {
		projected = publicMemoryProjected{
			searchText: projection.SearchText.Text, aliasCount: projection.SearchText.AliasCount,
			summary: mustCompactJSON(projection.Extraction),
			evidence: mustCompactJSON(map[string]any{
				"evidence_excerpts": stringsFromAny(projection.Extraction["evidence_excerpts"]),
			}),
			eligible: true,
		}
	}
	publicMemoryProjectionCache.Set(key, projected)
	return projected, projected.eligible
}

func releasePublicMemoryProjectionCache() {
	publicMemoryProjectionCache.InvalidateAll()
}

// memorySearchTextOf is memorySearchTextFromMemory(mem)'s text and alias
// count, kept with the memory's public projection.
func memorySearchTextOf(mem store.Memory) (string, int) {
	projected, _ := cachedPublicMemoryProjection(mem.SummaryJSON, mem.Evidence)
	return projected.searchText, projected.aliasCount
}

// memorySearchTextSet builds a list as repeated appendUniqueMemorySearchText
// calls do, keeping the existing items' keys so that each addition checks one
// key instead of every item.
type memorySearchTextSet struct {
	items []string
	keys  map[string]bool
}

func newMemorySearchTextSet(items []string) *memorySearchTextSet {
	set := &memorySearchTextSet{items: items, keys: make(map[string]bool, len(items))}
	for _, item := range items {
		set.keys[memorySearchTextKey(item)] = true
	}
	return set
}

func memorySearchTextKey(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
}

func (set *memorySearchTextSet) add(value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	key := memorySearchTextKey(value)
	if set.keys[key] {
		return
	}
	set.keys[key] = true
	set.items = append(set.items, value)
}

// memorySearchTextKeyEquals reports whether
// strings.ToLower(strings.Join(strings.Fields(text), " ")) == key without
// building that string: fields are separated by one space, runes are
// lowered with unicode.ToLower, and invalid UTF-8 bytes read as U+FFFD.
func memorySearchTextKeyEquals(text, key string) bool {
	pos := 0
	var buf [utf8.UTFMax]byte
	emit := func(b []byte) bool {
		if pos+len(b) > len(key) || key[pos:pos+len(b)] != string(b) {
			return false
		}
		pos += len(b)
		return true
	}
	inField, wroteField := false, false
	for i := 0; i < len(text); {
		// An invalid byte decodes as U+FFFD, which is not a space and lowers
		// to itself, matching what strings.ToLower writes for it.
		r, size := utf8.DecodeRuneInString(text[i:])
		i += size
		if unicode.IsSpace(r) {
			inField = false
			continue
		}
		if !inField {
			if wroteField && !emit([]byte{' '}) {
				return false
			}
			inField, wroteField = true, true
		}
		n := utf8.EncodeRune(buf[:], unicode.ToLower(r))
		if !emit(buf[:n]) {
			return false
		}
	}
	return pos == len(key)
}
