package httpapi

import (
	"context"
	"reflect"
	"testing"
)

func TestSummaryProgressShowsTheSummaryBeingMade(t *testing.T) {
	updates := []map[string]any{}
	ctx := withSummaryProgress(context.Background(), func(update map[string]any) { updates = append(updates, update) })
	reportSummaryInProgress(ctx, "chapter", 1, 60)
	reportSummaryInProgress(ctx, "chapter", 61, 120)
	reportSummaryInProgress(ctx, "arc", 1, 240)
	clearSummaryProgress(ctx)
	want := []map[string]any{
		{"current_summary": map[string]any{"layer": "chapter", "from_turn": 1, "to_turn": 60, "count": 1}},
		{"current_summary": map[string]any{"layer": "chapter", "from_turn": 61, "to_turn": 120, "count": 2}},
		{"current_summary": map[string]any{"layer": "arc", "from_turn": 1, "to_turn": 240, "count": 1}},
		{"current_summary": nil},
	}
	if !reflect.DeepEqual(updates, want) {
		t.Fatalf("updates = %+v", updates)
	}
	// Without a reporter (a turn, a manual regenerate) nothing is reported.
	reportSummaryInProgress(context.Background(), "saga", 1, 960)
	clearSummaryProgress(context.Background())
	if withSummaryProgress(context.Background(), nil) != context.Background() {
		t.Fatal("a nil reporter changed the context")
	}
}

func TestOpenTailIsMarkedAsWaitingWithItsCounts(t *testing.T) {
	layer := hierarchyLayerBackfillResult(false, "")
	addHierarchyBlocked(layer, 241, 255, "open_tail_episode_count")
	annotateHierarchyWaiting(layer, 3, 12)
	blocked := layer["blocked"].([]map[string]any)
	if blocked[0]["have"] != 3 || blocked[0]["need"] != 12 || blocked[0]["reason"] != "open_tail_episode_count" {
		t.Fatalf("blocked = %+v", blocked)
	}
	annotateHierarchyWaiting(hierarchyLayerBackfillResult(false, ""), 1, 4)
}
