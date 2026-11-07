package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/risulongmemory/archive-center-go/internal/store"
)

func resolvedThreadAssembly(threads []store.PendingThread) string {
	const sessionID = "resolved-thread"
	memories := []store.Memory{
		{ID: 1, ChatSessionID: sessionID, TurnIndex: 10, Importance: 8, SummaryJSON: `{"turn_summary":"Mira promised to meet Rook for lunch at the harbor tomorrow."}`},
		{ID: 2, ChatSessionID: sessionID, TurnIndex: 11, Importance: 6, SummaryJSON: `{"turn_summary":"The harbor bell was repainted blue."}`},
	}
	assembly := buildPrepareTurnInjectionAssemblyWithBudget(prepareTurnAssemblyInput{
		Memories:        memories,
		TopK:            5,
		MaxChars:        60000,
		UserInput:       "Mira thinks about Rook, lunch and the harbor.",
		Profile:         "default",
		VectorTrace:     map[string]any{"memory_search_result": "not_found", "search_result": "not_found"},
		BudgetMode:      "auto",
		Perspective:     testPrepareTurnAssemblyPerspective(priorityMemoryTestContext(5)),
		ResolvedThreads: threads,
	})
	return extractionStringFromAny(assembly.MemoryDeliveryPlan["final_text"])
}

func TestResolvedThreadMarksTheOldPlanAsDone(t *testing.T) {
	threads := []store.PendingThread{{
		ID: 7, ThreadKey: "lunch", Description: "Mira meets Rook for lunch at the harbor", Status: "resolved",
		CreatedTurn: 10, ResolvedTurn: 14, ResolutionNote: "They had lunch at the harbor.",
	}}
	text := resolvedThreadAssembly(threads)
	if !strings.Contains(text, "promised to meet Rook") || !strings.Contains(text, "resolved at turn 14; no longer pending") || !strings.Contains(text, "They had lunch at the harbor.") {
		t.Fatalf("the plan was delivered without its outcome: %q", text)
	}
	if strings.Count(text, "They had lunch at the harbor.") != 1 {
		t.Fatalf("the outcome was repeated: %q", text)
	}
	if without := resolvedThreadAssembly(nil); strings.Contains(without, "no longer pending") {
		t.Fatalf("an outcome appeared with no closed thread: %q", without)
	}
}

func TestResolvedThreadLeavesOtherMemoriesAlone(t *testing.T) {
	cases := map[string]store.PendingThread{
		// About something else entirely.
		"unrelated": {ID: 8, Description: "Sena repairs the mill wheel", CreatedTurn: 9, ResolvedTurn: 14},
		// Closed before the plan was made: a different, earlier lunch.
		"earlier": {ID: 9, Description: "Mira meets Rook for lunch at the harbor", CreatedTurn: 2, ResolvedTurn: 5},
	}
	for name, thread := range cases {
		if text := resolvedThreadAssembly([]store.PendingThread{thread}); strings.Contains(text, "no longer pending") {
			t.Errorf("%s: outcome attached: %q", name, text)
		}
	}
}

func TestResolvedThreadMatchesByLifecycleKey(t *testing.T) {
	out := &prepareTurnInjectionAssembly{PriorityFactSeeds: []prepareTurnPriorityFactSeed{
		{SourceTable: "memories", SourceTurn: 10, Fact: prepareTurnPriorityMemoryFact{Text: "A vague note", LifecycleKey: "harbor-lunch"}},
		{SourceTable: "world_rules", SourceTurn: 10, Fact: prepareTurnPriorityMemoryFact{Text: "A vague note", LifecycleKey: "harbor-lunch"}},
	}}
	prepareTurnAttachResolvedThreadContext(out, []store.PendingThread{{
		ID: 3, Description: "Lunch with Rook", CreatedTurn: 10, ResolvedTurn: 12, HookMetadataJSON: `{"lifecycle_key":"harbor-lunch"}`,
	}})
	if reading := out.PriorityFactSeeds[0].Fact.Reading; reading == nil || reading.Parts[len(reading.Parts)-1].Key != "@lifecycle/resolved_thread/3" {
		t.Fatalf("lifecycle key did not link the memory: %+v", reading)
	}
	if out.PriorityFactSeeds[1].Fact.Reading != nil {
		t.Fatal("a world rule was given a thread outcome")
	}
}

func TestResolvedThreadsKeepOnlyClosedBeforeThisTurn(t *testing.T) {
	s := &Server{Store: &resolvedThreadStore{threads: []store.PendingThread{
		{ID: 1, ResolvedTurn: 5}, {ID: 2, ResolvedTurn: 9}, {ID: 3, ResolvedTurn: 0}, {ID: 4, ResolvedTurn: 4, Suppressed: true},
	}}}
	got := s.prepareTurnResolvedThreads(t.Context(), "sid", 9)
	if len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("kept %+v", got)
	}
}

type resolvedThreadStore struct {
	store.Store
	threads []store.PendingThread
}

func (r *resolvedThreadStore) ListPendingThreads(_ context.Context, _, status string) ([]store.PendingThread, error) {
	if status != "resolved" {
		return nil, nil
	}
	return r.threads, nil
}
