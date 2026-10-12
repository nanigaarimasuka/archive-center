package httpapi

import (
	"reflect"
	"testing"
)

func TestLeanPrepareTurnMemoryDeliveryPlanOmitsOnlyDiagnosticItemText(t *testing.T) {
	item := map[string]any{"canonical_fact_id": "f1", "selection_status": "selected", "chars": 12,
		"rendered_text": "line", "minimum_context_text": "reading", "knowledge_boundaries": []any{"b"}}
	summary := map[string]any{"summary_id": "s1", "rendered_text": "turn", "knowledge_boundaries": []any{"b"}}
	plan := map[string]any{"contract_version": "v", "classes": []any{"long_term_memory"},
		"priority_items": []any{item}, "turn_summary_items": []map[string]any{summary}}

	lean := leanPrepareTurnMemoryDeliveryPlan(plan)

	gotItem := lean["priority_items"].([]any)[0].(map[string]any)
	if !reflect.DeepEqual(gotItem, map[string]any{"canonical_fact_id": "f1", "selection_status": "selected", "chars": 12}) {
		t.Fatalf("lean item = %#v", gotItem)
	}
	gotSummary := lean["turn_summary_items"].([]any)[0].(map[string]any)
	if !reflect.DeepEqual(gotSummary, map[string]any{"summary_id": "s1"}) {
		t.Fatalf("lean summary = %#v", gotSummary)
	}
	if lean["contract_version"] != "v" || !reflect.DeepEqual(lean["classes"], []any{"long_term_memory"}) {
		t.Fatalf("plan fields changed: %#v", lean)
	}
	if mapFromAny(lean["response_detail"])["mode"] != "lean" {
		t.Fatalf("response_detail missing: %#v", lean["response_detail"])
	}
	// Internal plans keep their texts for server-side readers.
	if item["rendered_text"] != "line" || item["minimum_context_text"] != "reading" || summary["rendered_text"] != "turn" {
		t.Fatal("source plan items were modified")
	}
	if _, ok := plan["response_detail"]; ok {
		t.Fatal("source plan was modified")
	}
}

func TestWithoutNestedKeyClonesOnlyTheChangedPath(t *testing.T) {
	trace := map[string]any{"memory_language_trace": map[string]any{"x": 1}, "kept": true}
	sibling := map[string]any{"n": 1}
	counts := map[string]any{"language_aware_injection": trace, "other": sibling}
	root := map[string]any{"counts": counts, "status": "ok"}

	out := withoutNestedKey(root, []string{"counts", "language_aware_injection"}, "memory_language_trace")

	gotTrace := out["counts"].(map[string]any)["language_aware_injection"].(map[string]any)
	if _, ok := gotTrace["memory_language_trace"]; ok || gotTrace["kept"] != true {
		t.Fatalf("trace = %#v", gotTrace)
	}
	if _, ok := trace["memory_language_trace"]; !ok {
		t.Fatal("source map was modified")
	}
	if reflect.ValueOf(out["counts"].(map[string]any)["other"]).Pointer() != reflect.ValueOf(sibling).Pointer() {
		t.Fatal("unchanged sibling was copied")
	}
	if same := withoutNestedKey(root, []string{"counts", "missing"}, "memory_language_trace"); reflect.ValueOf(same).Pointer() != reflect.ValueOf(root).Pointer() {
		t.Fatal("missing path should return the same map")
	}
}
