package httpapi

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/risulongmemory/archive-center-go/internal/dto"
	"github.com/risulongmemory/archive-center-go/internal/store"
)

// A past turn the user edited in RisuAI shows up in session normalize as a
// raw conflict: both messages are stored, but with other text. When the plugin
// lists the turn in apply_turn_edits, normalize takes RisuAI's text, clears
// what was derived from that turn, lets the rescan derive it again, and then
// rebuilds the episode, chapter, arc, and saga that hold the turn.

func (s *Server) applyNormalizeTurnEdits(ctx context.Context, sid string, req adminSessionNormalizeRequest, entries []dto.ChatLogRepairEntryRequest, conflictTurns []int) map[string]any {
	result := map[string]any{
		"status":       "skipped",
		"requested":    len(req.ApplyTurnEdits),
		"edited_turns": []int{},
		"candidates":   []int{},
		"skipped":      []map[string]any{},
		"failed":       []map[string]any{},
	}
	if len(req.ApplyTurnEdits) == 0 {
		result["reason"] = "no_turn_edits_requested"
		return result
	}
	editor, ok := s.Store.(store.HistoricalTurnEditStore)
	if !ok {
		result["reason"] = "turn_edit_store_unavailable"
		return result
	}
	conflicts := intsToSet(conflictTurns)
	entryByTurn := map[int]dto.ChatLogRepairEntryRequest{}
	for _, entry := range entries {
		entryByTurn[entry.TurnIndex] = entry
	}
	edited, candidates := []int{}, []int{}
	skipped, failed := []map[string]any{}, []map[string]any{}
	for _, turn := range uniqueSortedInts(req.ApplyTurnEdits) {
		if !conflicts[turn] {
			skipped = append(skipped, map[string]any{"turn_index": turn, "reason": "raw_not_in_conflict"})
			continue
		}
		entry, found := entryByTurn[turn]
		userText, assistantText := "", ""
		if found && entry.UserContent != nil {
			userText = sanitizeCriticStorageText(*entry.UserContent)
		}
		if found && entry.AssistantContent != nil {
			assistantText = sanitizeCriticStorageText(*entry.AssistantContent)
		}
		if strings.TrimSpace(userText) == "" || strings.TrimSpace(assistantText) == "" {
			skipped = append(skipped, map[string]any{"turn_index": turn, "reason": "edit_needs_both_messages"})
			continue
		}
		if req.DryRun {
			candidates = append(candidates, turn)
			continue
		}
		err := editor.ReviseHistoricalTurn(ctx, store.HistoricalTurnEdit{
			ChatSessionID: sid, TurnIndex: turn, UserContent: userText, AssistantContent: assistantText, EditedAt: time.Now().UTC(),
		})
		if err != nil {
			reason := "turn_edit_failed"
			if errors.Is(err, store.ErrNotFound) {
				reason = "turn_edit_message_missing"
			}
			failed = append(failed, map[string]any{"turn_index": turn, "reason": reason, "detail": err.Error()})
			continue
		}
		edited = append(edited, turn)
	}
	result["status"] = "ok"
	if len(failed) > 0 {
		result["status"] = "partial_error"
	}
	result["edited_turns"] = edited
	result["candidates"] = candidates
	result["skipped"] = skipped
	result["failed"] = failed
	return result
}

// regenerateHierarchyForEditedTurns rebuilds the summaries over the edited
// turns. A summary is replaced only once its new version has been made, so a
// failed LLM call leaves the old one in place.
func (s *Server) regenerateHierarchyForEditedTurns(ctx context.Context, sid string, turns []int, meta map[string]any) map[string]any {
	result := map[string]any{"status": "skipped", "turns": turns}
	if len(turns) == 0 || s.Store == nil {
		result["reason"] = "no_edited_turns"
		return result
	}
	targets := intsToSet(turns)
	logs, _ := s.Store.ListChatLogs(ctx, sid, 0, 0)
	memories, _ := s.Store.ListMemories(ctx, sid, 0, 0)
	evidence, _ := s.Store.ListEvidence(ctx, sid)
	episodeInterval := normalizedEpisodeInterval(intFromAny(meta["episode_interval_turns"], 0))
	// An episode is rebuilt only when every turn in it has its memory again;
	// until then the old one stays.
	result["episode"] = s.backfillEpisodeSummariesFromChatLogs(ctx, sid, logs, memories, evidence, episodeInterval, false, targets, true)
	result["chapter"] = s.regenerateChaptersForTurns(ctx, sid, targets)
	result["arc"] = s.regenerateArcsForTurns(ctx, sid, targets)
	result["saga"] = s.regenerateSagasForTurns(ctx, sid, targets)
	result["status"] = "ok"
	return result
}

type hierarchyRegenerationLayer struct {
	result  map[string]any
	deleter store.HierarchySummaryRangeDeleter
}

func (s *Server) newHierarchyRegenerationLayer() (*hierarchyRegenerationLayer, bool) {
	layer := &hierarchyRegenerationLayer{result: hierarchyLayerBackfillResult(false, "")}
	deleter, ok := s.Store.(store.HierarchySummaryRangeDeleter)
	if !ok {
		layer.result["reason"] = "hierarchy_range_delete_unavailable"
		return layer, false
	}
	layer.deleter = deleter
	layer.result["status"] = "ok"
	layer.result["reason"] = nil
	return layer, true
}

// replace swaps the old summary of a range for the new one.
func (l *hierarchyRegenerationLayer) replace(ctx context.Context, kind, sid string, fromTurn, toTurn int, save func() error) error {
	if _, err := l.deleter.DeleteHierarchySummaryRange(ctx, kind, sid, fromTurn, toTurn); err != nil {
		return err
	}
	if err := save(); err != nil {
		return err
	}
	l.result["generated"] = intFromAny(l.result["generated"], 0) + 1
	return nil
}

func rangeHoldsTargetTurn(fromTurn, toTurn int, targets map[int]bool) bool {
	for turn := range targets {
		if turn >= fromTurn && turn <= toTurn {
			return true
		}
	}
	return false
}

func childrenInRange[T summaryRanged](items []T, fromTurn, toTurn int) []T {
	out := []T{}
	seen := map[[2]int]bool{}
	for _, item := range items {
		from, to := summaryRangeOf(item)
		if from < fromTurn || to > toTurn || seen[[2]int{from, to}] {
			continue
		}
		seen[[2]int{from, to}] = true
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		fi, ti := summaryRangeOf(out[i])
		fj, tj := summaryRangeOf(out[j])
		if fi == fj {
			return ti < tj
		}
		return fi < fj
	})
	return out
}

func (s *Server) regenerateChaptersForTurns(ctx context.Context, sid string, targets map[int]bool) map[string]any {
	layer, ok := s.newHierarchyRegenerationLayer()
	chapterStore, storeOK := s.Store.(store.ChapterSummaryStore)
	if !ok || !storeOK {
		return layer.result
	}
	chapters, err := chapterStore.SearchChapterSummaries(ctx, sid, "", 0, 0, 0)
	if err != nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrNotEnabled) {
		layer.result["status"], layer.result["error"] = "partial_error", err.Error()
		return layer.result
	}
	episodes, _ := s.Store.ListEpisodeSummaries(ctx, sid, 0, 0, 0)
	for _, old := range childrenInRange(chapters, 1, int(^uint(0)>>1)) {
		if !rangeHoldsTargetTurn(old.FromTurn, old.ToTurn, targets) {
			continue
		}
		layer.result["candidate"] = intFromAny(layer.result["candidate"], 0) + 1
		group := childrenInRange(episodes, old.FromTurn, old.ToTurn)
		if !episodeCoverageComplete(group, old.FromTurn, old.ToTurn) {
			addHierarchyBlocked(layer.result, old.FromTurn, old.ToTurn, "blocked_missing_episode")
			continue
		}
		chapter, trace, genErr := s.buildChapterSummaryForRange(ctx, sid, old.FromTurn, old.ToTurn, old.ChapterIndex, group)
		if genErr != nil {
			addHierarchyFailed(layer.result, old.FromTurn, old.ToTurn, genErr, trace)
			continue
		}
		if err := layer.replace(ctx, "chapter", sid, old.FromTurn, old.ToTurn, func() error { return chapterStore.SaveChapterSummary(ctx, &chapter) }); err != nil {
			layer.result["status"], layer.result["error"] = "partial_error", err.Error()
		}
	}
	return layer.result
}

func (s *Server) regenerateArcsForTurns(ctx context.Context, sid string, targets map[int]bool) map[string]any {
	layer, ok := s.newHierarchyRegenerationLayer()
	arcStore, arcOK := s.Store.(store.ArcSummaryStore)
	chapterStore, chapterOK := s.Store.(store.ChapterSummaryStore)
	if !ok || !arcOK || !chapterOK {
		return layer.result
	}
	arcs, err := arcStore.SearchArcSummaries(ctx, sid, "", 0, 0, 0)
	if err != nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrNotEnabled) {
		layer.result["status"], layer.result["error"] = "partial_error", err.Error()
		return layer.result
	}
	chapters, _ := chapterStore.SearchChapterSummaries(ctx, sid, "", 0, 0, 0)
	for _, old := range childrenInRange(arcs, 1, int(^uint(0)>>1)) {
		if !rangeHoldsTargetTurn(old.FromTurn, old.ToTurn, targets) {
			continue
		}
		layer.result["candidate"] = intFromAny(layer.result["candidate"], 0) + 1
		group := childrenInRange(chapters, old.FromTurn, old.ToTurn)
		if !chapterCoverageComplete(group, old.FromTurn, old.ToTurn) {
			addHierarchyBlocked(layer.result, old.FromTurn, old.ToTurn, "blocked_missing_chapter")
			continue
		}
		arc, trace, genErr := s.buildArcSummaryForRange(ctx, sid, old.FromTurn, old.ToTurn, old.ArcIndex, group)
		if genErr != nil {
			addHierarchyFailed(layer.result, old.FromTurn, old.ToTurn, genErr, trace)
			continue
		}
		if err := layer.replace(ctx, "arc", sid, old.FromTurn, old.ToTurn, func() error { return arcStore.SaveArcSummary(ctx, sid, &arc) }); err != nil {
			layer.result["status"], layer.result["error"] = "partial_error", err.Error()
		}
	}
	return layer.result
}

func (s *Server) regenerateSagasForTurns(ctx context.Context, sid string, targets map[int]bool) map[string]any {
	layer, ok := s.newHierarchyRegenerationLayer()
	sagaStore, sagaOK := s.Store.(store.SagaDigestStore)
	arcStore, arcOK := s.Store.(store.ArcSummaryStore)
	if !ok || !sagaOK || !arcOK {
		return layer.result
	}
	sagas, err := sagaStore.SearchSagaDigests(ctx, sid, "", 0, 0, 0)
	if err != nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrNotEnabled) {
		layer.result["status"], layer.result["error"] = "partial_error", err.Error()
		return layer.result
	}
	arcs, _ := arcStore.SearchArcSummaries(ctx, sid, "", 0, 0, 0)
	for _, old := range childrenInRange(sagas, 1, int(^uint(0)>>1)) {
		if !rangeHoldsTargetTurn(old.FromTurn, old.ToTurn, targets) {
			continue
		}
		layer.result["candidate"] = intFromAny(layer.result["candidate"], 0) + 1
		group := childrenInRange(arcs, old.FromTurn, old.ToTurn)
		if !arcCoverageComplete(group, old.FromTurn, old.ToTurn) {
			addHierarchyBlocked(layer.result, old.FromTurn, old.ToTurn, "blocked_missing_arc")
			continue
		}
		saga, trace, genErr := s.buildSagaDigestForRange(ctx, sid, old.FromTurn, old.ToTurn, group)
		if genErr != nil {
			addHierarchyFailed(layer.result, old.FromTurn, old.ToTurn, genErr, trace)
			continue
		}
		if err := layer.replace(ctx, "saga", sid, old.FromTurn, old.ToTurn, func() error { return sagaStore.SaveSagaDigest(ctx, sid, &saga) }); err != nil {
			layer.result["status"], layer.result["error"] = "partial_error", err.Error()
		}
	}
	return layer.result
}
