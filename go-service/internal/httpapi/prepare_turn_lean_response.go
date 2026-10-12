package httpapi

import "maps"

// Normal-mode (non-debug) prepare-turn responses omit diagnostic text that the
// plugin never reads. The LLM payload and every selection decision are built
// before these projections, and debug requests keep the full response.

// Item fields of the memory delivery plan that only diagnostics read: each
// candidate's full reading, rendered line and structured boundaries.
var prepareTurnLeanOmittedItemFields = []string{"rendered_text", "minimum_context_text", "knowledge_boundaries"}

// leanPrepareTurnMemoryDeliveryPlan copies the plan without those item fields.
// Scores, ranks, selection status and reasons, and character counts remain.
func leanPrepareTurnMemoryDeliveryPlan(plan map[string]any) map[string]any {
	if plan == nil {
		return nil
	}
	lean := maps.Clone(plan)
	for _, group := range []string{"priority_items", "turn_summary_items"} {
		if _, ok := plan[group]; !ok {
			continue
		}
		items := prepareTurnMemoryLineageSlice(plan[group])
		projected := make([]any, len(items))
		for i, raw := range items {
			item, ok := raw.(map[string]any)
			if !ok {
				projected[i] = raw
				continue
			}
			trimmed := maps.Clone(item)
			for _, field := range prepareTurnLeanOmittedItemFields {
				delete(trimmed, field)
			}
			projected[i] = trimmed
		}
		lean[group] = projected
	}
	lean["response_detail"] = map[string]any{
		"mode":                "lean",
		"omitted_item_fields": prepareTurnLeanOmittedItemFields,
		"full_detail":         "debug mode (" + clientDebugHeader + ": 1)",
	}
	return lean
}

// withoutNestedKey returns m with key removed from the map at path, cloning
// only the maps along that path; m itself is never modified.
func withoutNestedKey(m map[string]any, path []string, key string) map[string]any {
	out, _ := withoutNestedKeyChanged(m, path, key)
	return out
}

func withoutNestedKeyChanged(m map[string]any, path []string, key string) (map[string]any, bool) {
	if len(path) == 0 {
		if _, ok := m[key]; !ok {
			return m, false
		}
		out := maps.Clone(m)
		delete(out, key)
		return out, true
	}
	child, ok := m[path[0]].(map[string]any)
	if !ok {
		return m, false
	}
	trimmed, changed := withoutNestedKeyChanged(child, path[1:], key)
	if !changed {
		return m, false
	}
	out := maps.Clone(m)
	out[path[0]] = trimmed
	return out, true
}
