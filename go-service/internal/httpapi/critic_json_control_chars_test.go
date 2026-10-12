package httpapi

import "testing"

func TestParseJSONFromLLMContentEscapesRawControlCharactersInStrings(t *testing.T) {
	content := "{\"turn_summary\":\"정리\",\"story_clock\":{\"evidence_excerpt\":\"🗓️ - 2026년 03월 29일\n🕰️ - 17시 04분\t20초\"},\"note\":\"a\\nb\"}"
	got, err := parseJSONFromLLMContent(content)
	if err != nil {
		t.Fatal(err)
	}
	if excerpt := mapFromAny(got["story_clock"])["evidence_excerpt"]; excerpt != "🗓️ - 2026년 03월 29일\n🕰️ - 17시 04분\t20초" {
		t.Fatalf("excerpt %q", excerpt)
	}
	if got["note"] != "a\nb" {
		t.Fatalf("an escaped line break changed: %q", got["note"])
	}
	// Line breaks between fields are not inside strings and stay as they are.
	if got, err := parseJSONFromLLMContent("{\n  \"a\": \"x\nY\",\n  \"b\": 1\n}"); err != nil || got["a"] != "x\nY" {
		t.Fatalf("%v %v", got, err)
	}
}
