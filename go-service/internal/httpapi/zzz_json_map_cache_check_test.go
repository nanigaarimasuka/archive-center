package httpapi

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"
)

// Every cached object is compared with a fresh parse of its text when the
// cache is released, so a shared reader that modified one is reported here,
// after the package's other tests have run.
var jsonMapCacheViolations struct {
	sync.Mutex
	texts []string
}

func init() {
	jsonMapCacheCheck = func(text string, cached map[string]any) {
		var fresh map[string]any
		if err := json.Unmarshal([]byte(text), &fresh); err != nil || !reflect.DeepEqual(fresh, cached) {
			jsonMapCacheViolations.Lock()
			jsonMapCacheViolations.texts = append(jsonMapCacheViolations.texts, text)
			jsonMapCacheViolations.Unlock()
		}
	}
}

func TestZZZJSONMapCacheEntriesWereNotModified(t *testing.T) {
	releaseJSONMapCache()
	jsonMapCacheViolations.Lock()
	defer jsonMapCacheViolations.Unlock()
	if len(jsonMapCacheViolations.texts) > 0 {
		t.Fatalf("%d cached JSON objects were modified by shared readers; first: %.300s", len(jsonMapCacheViolations.texts), jsonMapCacheViolations.texts[0])
	}
}

func TestParseJSONMapReturnsPrivateCopies(t *testing.T) {
	raw := `{"a":{"b":[1,{"c":"d"}]},"e":null,"f":[],"g":{}}`
	first := parseJSONMap(raw)
	first["a"].(map[string]any)["b"].([]any)[1].(map[string]any)["c"] = "changed"
	first["x"] = 1
	second := parseJSONMap(raw)
	var want map[string]any
	if err := json.Unmarshal([]byte(raw), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(second, want) {
		t.Fatalf("parseJSONMap returned a modified object: %#v", second)
	}
	if !reflect.DeepEqual(parseJSONMapShared(raw), want) {
		t.Fatal("shared object differs from a fresh parse")
	}
	for _, text := range []string{"", "  ", "not json", "[1,2]", "null", "{}"} {
		got, shared := parseJSONMap(text), parseJSONMapShared(text)
		if got == nil || shared == nil || len(got) != 0 || len(shared) != 0 {
			t.Fatalf("%q: got %#v / %#v, want empty maps", text, got, shared)
		}
	}
}
