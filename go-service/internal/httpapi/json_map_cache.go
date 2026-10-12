package httpapi

import (
	"encoding/json"
	"strings"
	"sync"
)

// Request assembly parses the same stored JSON (memory summaries, state
// evidence) many times. Parsed objects are cached by their text; callers get
// a deep copy, which is several times cheaper than parsing, unless they use
// parseJSONMapShared and only read. The cache is bounded and released when a
// prepare-turn request ends.
const jsonMapCacheMaxBytes = 64 << 20

var jsonMapCache = struct {
	sync.RWMutex
	entries map[string]map[string]any
	bytes   int
}{entries: map[string]map[string]any{}}

// jsonMapCacheCheck, when set (tests), sees every entry before it is
// released, to confirm that no shared reader modified it.
var jsonMapCacheCheck func(text string, cached map[string]any)

// parseJSONMapCached returns the cached object for text, parsing it on a
// miss. Empty or invalid text, and non-objects, return nil.
func parseJSONMapCached(text string) map[string]any {
	jsonMapCache.RLock()
	cached, ok := jsonMapCache.entries[text]
	jsonMapCache.RUnlock()
	if ok {
		return cached
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil || parsed == nil {
		return nil
	}
	jsonMapCache.Lock()
	defer jsonMapCache.Unlock()
	if existing, ok := jsonMapCache.entries[text]; ok {
		return existing
	}
	if jsonMapCache.bytes+len(text) > jsonMapCacheMaxBytes {
		releaseJSONMapCacheLocked()
	}
	jsonMapCache.entries[text] = parsed
	jsonMapCache.bytes += len(text)
	return parsed
}

// parseJSONMapShared is parseJSONMap without the copy. The result is shared:
// callers must only read it and must not keep it beyond the request.
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

func releaseJSONMapCache() {
	jsonMapCache.Lock()
	defer jsonMapCache.Unlock()
	releaseJSONMapCacheLocked()
}

func releaseJSONMapCacheLocked() {
	if jsonMapCacheCheck != nil {
		for text, cached := range jsonMapCache.entries {
			jsonMapCacheCheck(text, cached)
		}
	}
	jsonMapCache.entries = map[string]map[string]any{}
	jsonMapCache.bytes = 0
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
