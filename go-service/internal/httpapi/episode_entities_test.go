package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/risulongmemory/archive-center-go/internal/store"
)

func TestEpisodeSummaryNamesCharactersFromMemoriesAndSkipsHiddenNotes(t *testing.T) {
	note := "[//]: # (엘의 1인칭 POV 시점에서 묘사할 것. INPUT에 주어지지 않은 행동을 창작하지 말 것. 엘의 INPUT이 필요한 시점에서 정지할 것.)"
	episode, _ := buildEpisodeSummaryForRangeWithArtifacts(
		"sess-ko",
		391,
		395,
		[]store.ChatLog{
			{ChatSessionID: "sess-ko", TurnIndex: 391, Role: "assistant", Content: note + "\n\n남직원이 엘을 침대에 눕혔다."},
		},
		[]store.Memory{
			{ChatSessionID: "sess-ko", TurnIndex: 391, SummaryJSON: `{"turn_summary":"남직원이 엘을 침대에 눕혔다.","entities":{"characters":[{"name":"엘"},{"name":"남직원"}]}}`},
			{ChatSessionID: "sess-ko", TurnIndex: 392, SummaryJSON: `{"turn_summary":"사내가 나타났다.","character_deltas":[{"name":"남직원"},{"name":"사내"}]}`},
		},
		nil,
	)
	var entities []string
	if err := json.Unmarshal([]byte(episode.KeyEntities), &entities); err != nil {
		t.Fatal(err)
	}
	if strings.Join(entities, ",") != "엘,남직원,사내" {
		t.Fatalf("key entities = %v, want the memories' characters", entities)
	}

	// Without memories, the raw fallback still never reads the hidden note.
	raw, _ := buildEpisodeSummaryForRangeWithArtifacts("sess-ko", 391, 395,
		[]store.ChatLog{{ChatSessionID: "sess-ko", TurnIndex: 391, Role: "assistant", Content: note + "\nPOV Alpha met Bravo."}}, nil, nil)
	for _, field := range []string{raw.KeyEntities, raw.KeyEvents, raw.SummaryText} {
		if strings.Contains(field, "INPUT") || strings.Contains(field, "[//]") {
			t.Fatalf("hidden note leaked into the episode: %s", field)
		}
	}
	if !strings.Contains(raw.KeyEntities, "Alpha") {
		t.Fatalf("raw fallback lost the story's names: %s", raw.KeyEntities)
	}
}

func TestChapterListFieldsKeepTheModelsJSONItems(t *testing.T) {
	parsed := map[string]any{
		"callback_candidates": []any{"first callback", "second callback"},
		"open_loops":          []any{map[string]any{"id": "pink_letter", "detail": "a pink letter"}},
		"world_changes":       `["encoded"]`,
		"resume_text":         "plain text",
	}
	if got := compactChapterJSONField(parsed, "[]", "callback_candidates"); got != `["first callback","second callback"]` {
		t.Fatalf("list = %s", got)
	}
	if got := compactChapterJSONField(parsed, "[]", "open_loops"); got != `[{"detail":"a pink letter","id":"pink_letter"}]` {
		t.Fatalf("objects = %s", got)
	}
	if got := compactChapterJSONField(parsed, "[]", "world_changes"); got != `["encoded"]` {
		t.Fatalf("encoded string = %s", got)
	}
	if got := compactChapterJSONField(parsed, "[]", "resume_text"); got != `["plain text"]` {
		t.Fatalf("plain string = %s", got)
	}
	if got := compactChapterJSONField(parsed, `["fallback"]`, "missing"); got != `["fallback"]` {
		t.Fatalf("missing = %s", got)
	}
}
