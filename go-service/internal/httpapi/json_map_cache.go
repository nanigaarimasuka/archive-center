package httpapi

import (
	"encoding/json"
	"math"
	"os"
	"strings"

	"github.com/maypok86/otter/v2"
)

// Request assembly parses the same stored JSON (memory summaries, state
// evidence) many times. Parsed objects are cached by their text; callers get
// a deep copy, which is several times cheaper than parsing, unless they use
// parseJSONMapShared and only read. Text that is not a JSON object is cached
// too, as nil. The cache is bounded by the size of the cached text and
// released when a prepare-turn request ends.
//
// Parsed objects depend only on their text and are never changed, so they
// may also be kept across requests: AC_JSON_MAP_CACHE_KEEP=1 does that.
const jsonMapCacheMaxBytes = 64 << 20

var jsonMapCacheKeep = strings.TrimSpace(os.Getenv("AC_JSON_MAP_CACHE_KEEP")) == "1"

var jsonMapCache = otter.Must(&otter.Options[string, map[string]any]{
	MaximumWeight: jsonMapCacheMaxBytes,
	Weigher: func(text string, _ map[string]any) uint32 {
		return uint32(min(len(text), math.MaxUint32))
	},
	OnAtomicDeletion: func(e otter.DeletionEvent[string, map[string]any]) {
		if jsonMapCacheCheck != nil && e.Value != nil {
			jsonMapCacheCheck(e.Key, e.Value)
		}
	},
})

// jsonMapCacheCheck, when set (tests), sees every entry before it is
// released, to confirm that no shared reader modified it.
var jsonMapCacheCheck func(text string, cached map[string]any)

// parseJSONMapCached returns the cached object for text, parsing it on a
// miss. Empty or invalid text, and non-objects, return nil.
func parseJSONMapCached(text string) map[string]any {
	if cached, ok := jsonMapCache.GetIfPresent(text); ok {
		return cached
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		parsed = nil
	}
	cached, _ := jsonMapCache.SetIfAbsent(text, parsed)
	return cached
}

// parseJSONMapShared is parseJSONMap without the copy. The result is shared:
// callers must only read it and must not change it.
func parseJSONMapShared(raw string) map[string]any {
	text := strings.TrimSpace(raw)
	if text == "" {
		return map[string]any{}
	}
	if cached := parseJSONMapCached(text); cached != nil {
		return cached
	}
	return map[string]any{}
}

// releaseJSONMapCacheAfterRequest ends a prepare-turn request's use of the
// cache.
func releaseJSONMapCacheAfterRequest() {
	if !jsonMapCacheKeep {
		releaseJSONMapCache()
	}
}

func releaseJSONMapCache() {
	jsonMapCache.InvalidateAll()
}

// copyJSONValue deep-copies a value decoded by encoding/json into any:
// objects and arrays are copied, strings, numbers, booleans and nil shared.
func copyJSONValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = copyJSONValue(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = copyJSONValue(item)
		}
		return out
	default:
		return value
	}
}
