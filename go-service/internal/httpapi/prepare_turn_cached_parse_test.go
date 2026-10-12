package httpapi

import (
	"encoding/json"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/risulongmemory/archive-center-go/internal/store"
)

// refPrepareTurnMemorySummary is prepareTurnMemorySummary before it used the
// parse cache.
func refPrepareTurnMemorySummary(m store.Memory) string {
	summary := strings.TrimSpace(m.SummaryJSON)
	if summary == "" {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(summary), &parsed); err == nil {
		summary = memorySummaryFromParsed(parsed)
		if summary == "" {
			summary = publicMemoryProjectionNarrativeSummary(parsed)
		}
		if summary == "" {
			return ""
		}
	}
	placeParts := []string{}
	if wing := strings.TrimSpace(m.PlaceWing); wing != "" {
		placeParts = append(placeParts, "archive_wing="+wing)
	}
	if room := strings.TrimSpace(m.PlaceRoom); room != "" {
		placeParts = append(placeParts, "archive_room="+room)
	}
	if len(placeParts) > 0 {
		summary = strings.TrimSpace(summary + " (" + strings.Join(placeParts, ", ") + ")")
	}
	return compactPrepareTurnLine(summary, 0)
}

// refParseSurfacePayload is parseSurfacePayload before it used the parse
// cache.
func refParseSurfacePayload(raw string) any {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err == nil {
		return parsed
	}
	return text
}

var cachedParseTestTexts = []string{
	"", " ", "null", " null ", "nul", "{}", " {} ", "[]", "[1,2]", `"text"`, "7", "true", "{bad", "{}x", `{"a":1}{"b":2}`,
	`{"turn_summary":"Mina returned the key."}`,
	`{"summary":"  "}`,
	`{"scene_summary":"{\"nested\":1}","content":"fallback"}`,
	`{"narrative_events":[{"summary":"Kael left the guard."},{"event":"Rin hid the map."}]}`,
	`{"interaction_events":[{"subject":"Mina","target_entity":"Kael","summary":"argued"}]}`,
	`{"turn_summary":null,"text":"plain"}`,
	"plain prose, not JSON",
	`{"a":{"b":[1,{"c":"d"}]},"e":null}`,
	"\xef\xbb\xbf{}",
}

func TestPrepareTurnMemorySummaryMatchesDirectParse(t *testing.T) {
	r := rand.New(rand.NewSource(71))
	for c := 0; c < 3000; c++ {
		m := store.Memory{SummaryJSON: cachedParseTestTexts[r.Intn(len(cachedParseTestTexts))]}
		if r.Intn(2) == 0 {
			m.SummaryJSON = " " + m.SummaryJSON + "\n"
		}
		if r.Intn(3) == 0 {
			m.PlaceWing = []string{"", " east ", "west"}[r.Intn(3)]
			m.PlaceRoom = []string{"", "hall"}[r.Intn(2)]
		}
		if got, want := prepareTurnMemorySummary(m), refPrepareTurnMemorySummary(m); got != want {
			t.Fatalf("case %d %+v: %q, want %q", c, m, got, want)
		}
	}
}

func TestParseSurfacePayloadMatchesDirectParse(t *testing.T) {
	for _, text := range cachedParseTestTexts {
		for _, raw := range []string{text, " " + text + "\t"} {
			got, want := parseSurfacePayload(raw), refParseSurfacePayload(raw)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%q: %#v, want %#v", raw, got, want)
			}
			// The result is the caller's own: changing it leaves later
			// parses unchanged.
			if object, ok := got.(map[string]any); ok {
				object["changed"] = true
				if again := parseSurfacePayload(raw); !reflect.DeepEqual(again, want) {
					t.Fatalf("%q: a caller's change leaked into the cache: %#v", raw, again)
				}
			}
		}
	}
}
