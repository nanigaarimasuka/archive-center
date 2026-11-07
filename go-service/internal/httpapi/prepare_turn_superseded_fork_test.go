package httpapi

import (
	"strings"
	"testing"

	"github.com/risulongmemory/archive-center-go/internal/store"
)

func TestSupersededMemoryCarriesTheLaterChange(t *testing.T) {
	const sessionID = "superseded"
	memories := []store.Memory{
		{ID: 1, ChatSessionID: sessionID, TurnIndex: 10, Importance: 8, SummaryJSON: `{"turn_summary":"Mira and Rook will have lunch at the harbor tomorrow."}`},
		{ID: 2, ChatSessionID: sessionID, TurnIndex: 14, Importance: 5, SummaryJSON: `{"turn_summary":"Mira moved the lunch with Rook to dinner.","superseded_memories":[{"memory_id":1,"reason":"The harbor lunch was moved to dinner."}]}`},
	}
	assembly := buildPrepareTurnInjectionAssemblyWithBudget(prepareTurnAssemblyInput{
		Memories:    memories,
		TopK:        5,
		MaxChars:    60000,
		UserInput:   "Mira thinks about lunch with Rook at the harbor.",
		Profile:     "default",
		VectorTrace: map[string]any{"memory_search_result": "not_found", "search_result": "not_found"},
		BudgetMode:  "auto",
		Perspective: testPrepareTurnAssemblyPerspective(priorityMemoryTestContext(5)),
	})
	text := extractionStringFromAny(assembly.MemoryDeliveryPlan["final_text"])
	if !strings.Contains(text, "lunch at the harbor tomorrow") || !strings.Contains(text, "turn 14; this memory is out of date") || !strings.Contains(text, "moved to dinner.") {
		t.Fatalf("the outdated plan was delivered without its later change: %q", text)
	}
}

func TestSupersessionsNeedAKnownEarlierMemory(t *testing.T) {
	memories := []store.Memory{
		{ID: 1, TurnIndex: 10, SummaryJSON: `{"turn_summary":"a"}`},
		{ID: 2, TurnIndex: 12, SummaryJSON: `{"superseded_memories":[{"memory_id":1,"reason":"ok"},{"memory_id":2},{"memory_id":3},{"memory_id":99},{"memory_id":"x"}]}`},
		{ID: 3, TurnIndex: 15, SummaryJSON: `{"turn_summary":"c"}`},
		{ID: 4, TurnIndex: 12, SummaryJSON: `{"superseded_memories":[{"memory_id":2,"reason":"same turn"}]}`},
	}
	got := prepareTurnSupersessions(memories)
	if len(got) != 1 || len(got[1]) != 1 || got[1][0].byID != 2 || got[1][0].turn != 12 || got[1][0].reason != "ok" {
		t.Fatalf("supersessions = %+v", got)
	}
}
