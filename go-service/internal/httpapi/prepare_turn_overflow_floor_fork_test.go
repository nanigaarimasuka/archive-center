package httpapi

import (
	"strings"
	"testing"
)

// The upstream delivery tests describe filling the whole budget; they run
// without the overflow floor, and the tests below turn it on.
func init() { prepareTurnOverflowMinRelevance = 0 }

func withOverflowRelevanceFloor(t *testing.T) {
	t.Helper()
	prepareTurnOverflowMinRelevance = prepareTurnOverflowMinRelevanceDefault
	t.Cleanup(func() { prepareTurnOverflowMinRelevance = 0 })
}

func overflowFloorPlan(similarities ...float64) map[string]any {
	texts := []string{"Mira kept the brass key.", "Rook sold grain at the gate.", "The bell rang at noon."}
	out := &prepareTurnInjectionAssembly{Counts: map[string]any{}}
	for i, similarity := range similarities {
		out.PriorityFactSeeds = append(out.PriorityFactSeeds, prepareTurnPriorityFactSeed{
			Lane: "event_recent", SourceTable: "memories", Tier: "required", SourceTurn: 10 + i,
			Importance: 0.5, ImportancePresent: true,
			SemanticSimilarity: similarity, SemanticSimilarityObserved: true,
			Fact: prepareTurnPriorityMemoryFact{Text: texts[i]},
		})
	}
	return buildPrepareTurnMemoryDeliveryPlan(out, 4000, testPrepareTurnMemorySelectionContext(map[string]any{
		"_priority_memory_enabled": true, "_priority_memory_max_items": 1,
		"_priority_memory_query":        "Where is the brass key?",
		"_priority_memory_current_turn": 20,
	}))
}

func TestOverflowFloorKeepsBarelyMatchingMemoriesOut(t *testing.T) {
	withOverflowRelevanceFloor(t)
	text := extractionStringFromAny(overflowFloorPlan(0.9, 0.1, 0.5)["final_text"])
	if !strings.Contains(text, "brass key") || !strings.Contains(text, "bell rang") {
		t.Fatalf("relevant memories missing: %q", text)
	}
	if strings.Contains(text, "sold grain") {
		t.Fatalf("a barely matching memory filled the spare budget: %q", text)
	}
}

func TestOverflowFloorLeavesTheCoreItemIn(t *testing.T) {
	withOverflowRelevanceFloor(t)
	// Every group still gets its core turn, however weak its best match is.
	text := extractionStringFromAny(overflowFloorPlan(0.1)["final_text"])
	if !strings.Contains(text, "brass key") {
		t.Fatalf("the core item was dropped: %q", text)
	}
}

func TestOverflowFloorSkipsStateAndPreprocessingChoices(t *testing.T) {
	withOverflowRelevanceFloor(t)
	cases := []struct {
		source, lane string
		selection    *multiAgentSelection
		want         bool
	}{
		{"memories", "event_recent", nil, true},
		{"precise_memory_units", "event_recent", nil, true},
		{"world_rules", "world_state", nil, false},
		{"pending_threads", "unresolved_goal", nil, false},
		{"memories", "event_recent", &multiAgentSelection{Roles: []multiAgentRoleResult{{Role: "event_recent", Source: "ai"}}}, false},
		{"memories", "event_recent", &multiAgentSelection{Roles: []multiAgentRoleResult{{Role: "event_recent", Source: "go_default"}}}, true},
	}
	for _, c := range cases {
		if got := prepareTurnOverflowBelowFloor(&prepareTurnInjectionAssembly{Preprocessing: c.selection}, c.lane, c.lane, c.source, 0.1); got != c.want {
			t.Errorf("%s/%s with %+v: below floor = %v, want %v", c.source, c.lane, c.selection, got, c.want)
		}
	}
	if prepareTurnOverflowBelowFloor(&prepareTurnInjectionAssembly{}, "event_recent", "body_tracking", "memories", 0) {
		t.Error("a body reading was held to the floor")
	}
}

func TestOverflowFloorFollowsTheSetting(t *testing.T) {
	withOverflowRelevanceFloor(t)
	setting := func(v float64) *prepareTurnInjectionAssembly {
		return &prepareTurnInjectionAssembly{overflowMinRelevance: &v}
	}
	for _, c := range []struct {
		out  *prepareTurnInjectionAssembly
		want float64
	}{{nil, 0.3}, {&prepareTurnInjectionAssembly{}, 0.3}, {setting(0), 0}, {setting(0.55), 0.55}, {setting(1.7), 1}} {
		if got := prepareTurnOverflowFloor(c.out); got != c.want {
			t.Errorf("floor = %v, want %v", got, c.want)
		}
	}
	if prepareTurnOverflowBelowFloor(setting(0), "event_recent", "event_recent", "memories", 0.01) {
		t.Error("a floor of 0 still held a memory out")
	}
	if !prepareTurnOverflowBelowFloor(setting(0.6), "event_recent", "event_recent", "memories", 0.5) {
		t.Error("a raised floor let a weaker memory in")
	}
}

func TestOverflowFloorSettingIsSyncedAndKept(t *testing.T) {
	s := &Server{}
	s.updateRuntimeConfig(map[string]any{"memoryOverflowMinRelevance": 0.45})
	got := s.runtimeConfigSnapshot().MemoryOverflowMinRelevance
	if got == nil || *got != 0.45 || s.runtimeConfigSaved["memoryOverflowMinRelevance"] == nil {
		t.Fatalf("setting = %v, saved = %v", got, s.runtimeConfigSaved)
	}
}
