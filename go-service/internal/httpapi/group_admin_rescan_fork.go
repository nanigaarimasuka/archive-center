package httpapi

// addHierarchyFailed records a range whose LLM summary failed. Nothing is
// saved for it, so the next run finds the range missing and tries again.
func addHierarchyFailed(layer map[string]any, fromTurn, toTurn int, err error, trace map[string]any) {
	layer["failed"] = intFromAny(layer["failed"], 0) + 1
	addHierarchyBlocked(layer, fromTurn, toTurn, "llm_failed")
	if blocked, ok := layer["blocked"].([]map[string]any); ok && len(blocked) > 0 {
		blocked[len(blocked)-1]["llm_error"] = err.Error()
		blocked[len(blocked)-1]["generation_source"] = trace["generation_source"]
	}
}
