package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/risulongmemory/archive-center-go/internal/store"
)

// Stored chapters, arcs, and sagas that should be made again:
//   - broken_list_fields: a list field holds one string that is Go's printout
//     of a list ("[...]" or "map[...]"), left by an old bug;
//   - rule_based_summary: the rule-based stand-in once saved when the LLM
//     failed, known by its placeholder title together with its "Turns a-b:"
//     resume line;
//   - rule_based_fields: an arc whose name and core conflict are rule-based
//     text because the old prompt never asked for them.
// Session normalize rebuilds them when the user agrees, and a rebuilt summary
// takes the summaries above it along so they never go stale.

type hierarchyRebuildTarget struct {
	Layer    string   `json:"layer"`
	FromTurn int      `json:"from_turn"`
	ToTurn   int      `json:"to_turn"`
	Reasons  []string `json:"reasons"`
}

func listFieldLooksGoFormatted(raw string) bool {
	var items []any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &items); err != nil {
		return false
	}
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			continue
		}
		text = strings.TrimSpace(text)
		if strings.HasPrefix(text, "map[") || (strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]")) {
			return true
		}
	}
	return false
}

func anyListFieldLooksGoFormatted(fields ...string) bool {
	for _, field := range fields {
		if listFieldLooksGoFormatted(field) {
			return true
		}
	}
	return false
}

func ruleBasedResume(resume string, fromTurn, toTurn int) bool {
	return strings.HasPrefix(strings.TrimSpace(resume), fmt.Sprintf("Turns %d-%d:", fromTurn, toTurn))
}

// ruleBasedDenseText reports text built from labelled anchor lines, as the
// rule-based arc fields are.
func ruleBasedDenseText(text string) bool {
	text = strings.TrimSpace(text)
	for _, label := range []string{"open_loop:", "relationship:", "world:", "callback:", "resume:", "summary:"} {
		if strings.HasPrefix(text, label) {
			return true
		}
	}
	return false
}

func chapterRebuildReasons(item store.ChapterSummary) []string {
	reasons := []string{}
	if anyListFieldLooksGoFormatted(item.OpenLoopsJSON, item.RelationshipChangesJSON, item.WorldChangesJSON, item.CallbackCandidatesJSON) {
		reasons = append(reasons, "broken_list_fields")
	}
	if strings.TrimSpace(item.ChapterTitle) == fmt.Sprintf("Chapter %d", item.ChapterIndex) && ruleBasedResume(item.ResumeText, item.FromTurn, item.ToTurn) {
		reasons = append(reasons, "rule_based_summary")
	}
	return reasons
}

func arcRebuildReasons(item store.ArcSummary) []string {
	reasons := []string{}
	if anyListFieldLooksGoFormatted(item.KeyTurningPointsJSON, item.ActivePromisesJSON, item.UnresolvedDebtsJSON, item.ResolvedPayoffsJSON,
		item.CallbackCandidatesJSON, item.FuturePayoffCandidatesJSON, item.IrreversibleTurnsJSON, item.CallbackDebtsJSON, item.RelationshipPivotsJSON) {
		reasons = append(reasons, "broken_list_fields")
	}
	placeholderName := strings.TrimSpace(item.ArcName) == fmt.Sprintf("Arc %d", item.ArcIndex)
	if placeholderName && ruleBasedResume(item.ArcResumeText, item.FromTurn, item.ToTurn) {
		reasons = append(reasons, "rule_based_summary")
	} else if placeholderName && ruleBasedDenseText(item.CoreConflict) {
		// The old arc prompt did not ask for a name or a core conflict, so the
		// rule-based text filled them in even when the LLM answered.
		reasons = append(reasons, "rule_based_fields")
	}
	return reasons
}

func sagaRebuildReasons(item store.SagaDigest) []string {
	reasons := []string{}
	if anyListFieldLooksGoFormatted(item.PersistentFactsJSON, item.NeverDropCandidatesJSON) {
		reasons = append(reasons, "broken_list_fields")
	}
	if strings.TrimSpace(item.EraLabel) == fmt.Sprintf("Era %d-%d", item.FromTurn, item.ToTurn) && ruleBasedResume(item.ResumePackText, item.FromTurn, item.ToTurn) {
		reasons = append(reasons, "rule_based_summary")
	}
	return reasons
}

// hierarchyRebuildTargets lists the stored summaries to make again, and the
// summaries above them, which are rebuilt along with them.
func (s *Server) hierarchyRebuildTargets(ctx context.Context, sid string) []hierarchyRebuildTarget {
	targets := []hierarchyRebuildTarget{}
	if s == nil || s.Store == nil {
		return targets
	}
	rebuiltRanges := [][2]int{}
	coversRebuilt := func(fromTurn, toTurn int) bool {
		for _, r := range rebuiltRanges {
			if r[0] >= fromTurn && r[1] <= toTurn {
				return true
			}
		}
		return false
	}
	add := func(layer string, fromTurn, toTurn int, reasons []string) {
		if coversRebuilt(fromTurn, toTurn) {
			reasons = append(reasons, "contains_rebuilt_summary")
		}
		if len(reasons) == 0 {
			return
		}
		targets = append(targets, hierarchyRebuildTarget{Layer: layer, FromTurn: fromTurn, ToTurn: toTurn, Reasons: reasons})
		rebuiltRanges = append(rebuiltRanges, [2]int{fromTurn, toTurn})
	}
	if chapterStore, ok := s.Store.(store.ChapterSummaryStore); ok {
		chapters, _ := chapterStore.SearchChapterSummaries(ctx, sid, "", 0, 0, 0)
		for _, item := range childrenInRange(chapters, 1, int(^uint(0)>>1)) {
			add("chapter", item.FromTurn, item.ToTurn, chapterRebuildReasons(item))
		}
	}
	if arcStore, ok := s.Store.(store.ArcSummaryStore); ok {
		arcs, _ := arcStore.SearchArcSummaries(ctx, sid, "", 0, 0, 0)
		for _, item := range childrenInRange(arcs, 1, int(^uint(0)>>1)) {
			add("arc", item.FromTurn, item.ToTurn, arcRebuildReasons(item))
		}
	}
	if sagaStore, ok := s.Store.(store.SagaDigestStore); ok {
		sagas, _ := sagaStore.SearchSagaDigests(ctx, sid, "", 0, 0, 0)
		for _, item := range childrenInRange(sagas, 1, int(^uint(0)>>1)) {
			add("saga", item.FromTurn, item.ToTurn, sagaRebuildReasons(item))
		}
	}
	return targets
}

// rebuildHierarchyFrom makes the given layer's summaries over the target
// turns again and then the summaries above them. A layer above is rebuilt
// only where the layer below was rebuilt, so a failure leaves everything
// above it as it was.
func (s *Server) rebuildHierarchyFrom(ctx context.Context, sid, layer string, targets map[int]bool) map[string]any {
	result := map[string]any{"status": "ok"}
	rebuilt := targets
	layers := []string{"chapter", "arc", "saga"}
	start := -1
	for i, name := range layers {
		if name == layer {
			start = i
		}
	}
	if start < 0 {
		result["status"], result["reason"] = "skipped", "unknown_layer"
		return result
	}
	for _, name := range layers[start:] {
		if len(rebuilt) == 0 {
			break
		}
		var layerResult map[string]any
		switch name {
		case "chapter":
			layerResult = s.regenerateChaptersForTurns(ctx, sid, rebuilt)
		case "arc":
			layerResult = s.regenerateArcsForTurns(ctx, sid, rebuilt)
		case "saga":
			layerResult = s.regenerateSagasForTurns(ctx, sid, rebuilt)
		}
		result[name] = layerResult
		if intFromAny(layerResult["failed"], 0) > 0 || stringFromMap(layerResult, "status") == "partial_error" {
			result["status"] = "partial_error"
		}
		if intFromAny(layerResult["generated"], 0) == 0 {
			break
		}
		// The layer above is rebuilt where this one was; a range that failed or
		// was blocked does not carry upward.
		blocked, _ := layerResult["blocked"].([]map[string]any)
		next := map[int]bool{}
		for turn := range rebuilt {
			held := false
			for _, item := range blocked {
				if turn >= intFromAny(item["from_turn"], 0) && turn <= intFromAny(item["to_turn"], 0) {
					held = true
				}
			}
			if !held {
				next[turn] = true
			}
		}
		rebuilt = next
	}
	return result
}

// rebuildFlaggedHierarchy rebuilds what hierarchyRebuildTargets lists.
func (s *Server) rebuildFlaggedHierarchy(ctx context.Context, sid string) map[string]any {
	targets := s.hierarchyRebuildTargets(ctx, sid)
	result := map[string]any{"status": "ok", "targets": targets}
	if len(targets) == 0 {
		result["status"], result["reason"] = "skipped", "nothing_to_rebuild"
		return result
	}
	// climbed holds, per layer, the turns a climb from below already rebuilt.
	climbed := map[string][]int{}
	for _, layer := range []string{"chapter", "arc", "saga"} {
		turns := map[int]bool{}
		for _, target := range targets {
			if target.Layer != layer || hierarchyTargetOnlyContainsRebuilt(target) || rangeHoldsAnyTurn(target.FromTurn, target.ToTurn, climbed[layer]) {
				continue
			}
			turns[target.FromTurn] = true
		}
		if len(turns) == 0 {
			continue
		}
		layerResult := s.rebuildHierarchyFrom(ctx, sid, layer, turns)
		result[layer] = layerResult
		for _, above := range []string{"arc", "saga"} {
			if intFromAny(mapFromAny(layerResult[above])["generated"], 0) > 0 {
				for turn := range turns {
					climbed[above] = append(climbed[above], turn)
				}
			}
		}
		if stringFromMap(layerResult, "status") != "ok" {
			result["status"] = "partial_error"
		}
	}
	return result
}

// A summary listed only because one below it is rebuilt is handled by that
// rebuild's climb, not on its own.
func hierarchyTargetOnlyContainsRebuilt(target hierarchyRebuildTarget) bool {
	return len(target.Reasons) == 1 && target.Reasons[0] == "contains_rebuilt_summary"
}

// handleHierarchyRegenerate rebuilds one stored chapter, arc, or saga on the
// user's request, and the summaries above it.
func (s *Server) handleHierarchyRegenerate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChatSessionID string `json:"chat_session_id"`
		Layer         string `json:"layer"`
		FromTurn      int    `json:"from_turn"`
		ToTurn        int    `json:"to_turn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	sid := strings.TrimSpace(req.ChatSessionID)
	layer := strings.TrimSpace(req.Layer)
	if sid == "" || req.FromTurn <= 0 || req.ToTurn < req.FromTurn {
		writeBadRequest(w, "chat_session_id, from_turn and to_turn are required")
		return
	}
	if layer != "chapter" && layer != "arc" && layer != "saga" {
		writeBadRequest(w, "layer must be chapter, arc, or saga")
		return
	}
	if !s.usesShadowWriteStore() {
		writeShadowGuard(w, "POST /admin/hierarchy/regenerate")
		return
	}
	result := s.rebuildHierarchyFrom(r.Context(), sid, layer, map[int]bool{req.FromTurn: true})
	layerResult := mapFromAny(result[layer])
	regenerated := intFromAny(layerResult["generated"], 0) > 0
	status := "ok"
	if !regenerated {
		status = "error"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":          status,
		"chat_session_id": sid,
		"layer":           layer,
		"from_turn":       req.FromTurn,
		"to_turn":         req.ToTurn,
		"regenerated":     regenerated,
		"rebuild":         result,
	})
}

// cascadeAfterEpisodeRebuild remakes the chapter holding a rebuilt episode
// and what is above it, so they do not keep describing the old episode.
func (s *Server) cascadeAfterEpisodeRebuild(ctx context.Context, sid string, fromTurn int) map[string]any {
	return s.rebuildHierarchyFrom(ctx, sid, "chapter", map[int]bool{fromTurn: true})
}

func rangeHoldsAnyTurn(fromTurn, toTurn int, turns []int) bool {
	for _, turn := range turns {
		if turn >= fromTurn && turn <= toTurn {
			return true
		}
	}
	return false
}
