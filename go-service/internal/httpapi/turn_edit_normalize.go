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
// rebuilds the episode, chapter, arc, and saga that hold the turn. Records the
// turn shares with other turns are kept and listed in review_items; the user
// can then replay from the turn (replay_from_turn), which derives every later
// turn again too.

func (s *Server) applyNormalizeTurnEdits(ctx context.Context, sid string, req adminSessionNormalizeRequest, entries []dto.ChatLogRepairEntryRequest, conflictTurns []int) map[string]any {
	result := map[string]any{
		"status":       "skipped",
		"requested":    len(req.ApplyTurnEdits),
		"edited_turns": []int{},
		"candidates":   []int{},
		"skipped":      []map[string]any{},
		"failed":       []map[string]any{},
		"review_items": []store.HistoricalTurnReviewItem{},
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
	reviewItems := []store.HistoricalTurnReviewItem{}
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
		review, err := editor.ReviseHistoricalTurn(ctx, store.HistoricalTurnEdit{
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
		reviewItems = append(reviewItems, review...)
	}
	result["status"] = "ok"
	if len(failed) > 0 {
		result["status"] = "partial_error"
	}
	result["edited_turns"] = edited
	result["candidates"] = candidates
	result["skipped"] = skipped
	result["failed"] = failed
	result["review_items"] = reviewItems
	if len(reviewItems) > 0 {
		result["replay_candidate"] = s.replayCandidate(ctx, sid, reviewItems)
	}
	return result
}

// replayCandidate is the replay that would settle the review items: from the
// earliest edited turn they name to the last stored turn.
func (s *Server) replayCandidate(ctx context.Context, sid string, items []store.HistoricalTurnReviewItem) map[string]any {
	from := 0
	for _, item := range items {
		if from == 0 || item.TurnIndex < from {
			from = item.TurnIndex
		}
	}
	logs, _ := s.Store.ListChatLogs(ctx, sid, 0, 0)
	turns := map[int]bool{}
	last := from
	for _, log := range logs {
		if log.TurnIndex >= from {
			turns[log.TurnIndex] = true
			last = maxInt(last, log.TurnIndex)
		}
	}
	return map[string]any{"from_turn": from, "last_turn": last, "turns": len(turns)}
}

// clearDerivedForReplay clears what was derived from req.ReplayFromTurn
// onward and returns the turns to derive again. The raw text stays, and so do
// the records the user pinned or corrected.
func (s *Server) clearDerivedForReplay(ctx context.Context, sid string, req adminSessionNormalizeRequest) (map[string]any, []int, error) {
	from := req.ReplayFromTurn
	result := map[string]any{"status": "skipped", "from_turn": from}
	if from <= 0 || s.Store == nil {
		result["reason"] = "not_requested"
		return result, nil, nil
	}
	logs, err := s.Store.ListChatLogs(ctx, sid, 0, 0)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, nil, err
	}
	turns := []int{}
	for _, log := range logs {
		if log.TurnIndex >= from {
			turns = append(turns, log.TurnIndex)
		}
	}
	turns = uniqueSortedNonNegativeInts(turns)
	result["turns"] = len(turns)
	if len(turns) == 0 {
		result["reason"] = "no_turns_from_replay_turn"
		return result, nil, nil
	}
	result["last_turn"] = turns[len(turns)-1]
	if req.DryRun {
		result["status"] = "dry_run"
		return result, turns, nil
	}
	replayer, ok := s.Store.(store.DerivedReplayStore)
	if !ok {
		result["status"], result["reason"] = "error", "replay_store_unavailable"
		return result, nil, nil
	}
	if err := replayer.ClearDerivedFromTurn(ctx, sid, from, time.Now().UTC()); err != nil {
		return nil, nil, err
	}
	result["status"] = "ok"
	return result, turns, nil
}

// turnsBefore keeps the turns before limit; limit <= 0 keeps them all.
func turnsBefore(turns []int, limit int) []int {
	if limit <= 0 {
		return turns
	}
	kept := []int{}
	for _, turn := range turns {
		if turn < limit {
			kept = append(kept, turn)
		}
	}
	return kept
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

// normalizeInOrder is the in-order rescan session normalize runs with, so a
// turn that keeps failing stops it instead of letting later turns go first.
func normalizeInOrder(dryRun bool) *adminRescanInOrder {
	if dryRun {
		return nil
	}
	return &adminRescanInOrder{}
}

// settleNormalizeRescan brings the rescan result up to date before normalize
// reports it: failed turns the memory worker has derived since count as
// recovered, retried turns are listed, and a stopped rescan says where to
// resume. A stopped replay is resumed by replaying from that turn.
func (s *Server) settleNormalizeRescan(ctx context.Context, sid string, rescanResult, replayResult map[string]any, inOrder *adminRescanInOrder, replayTurns []int) {
	s.settleRecoveredFailures(ctx, sid, rescanResult)
	if inOrder == nil || rescanResult == nil {
		return
	}
	inOrder.settle(rescanResult)
	if len(inOrder.Retried) > 0 {
		rescanResult["retried_turns"] = inOrder.Retried
	}
	if inOrder.StoppedAt <= 0 {
		return
	}
	remaining := maxInt(1, intFromAny(rescanResult["candidate_count"], 0)-intFromAny(rescanResult["succeeded"], 0)-intFromAny(rescanResult["skipped"], 0))
	if len(replayTurns) > 0 && replayResult != nil {
		remaining = 0
		for _, turn := range replayTurns {
			if turn >= inOrder.StoppedAt {
				remaining++
			}
		}
		replayResult["status"] = "stopped"
		replayResult["stopped_at_turn"] = inOrder.StoppedAt
		replayResult["remaining_turns"] = remaining
	}
	rescanResult["stopped_at_turn"] = inOrder.StoppedAt
	rescanResult["remaining_turns"] = remaining
}

// settleRecoveredFailures moves the failed turns that have memories by now,
// because the memory worker retried them after the rescan, to recovered_turns.
func (s *Server) settleRecoveredFailures(ctx context.Context, sid string, rescanResult map[string]any) {
	failedItems := adminSessionNormalizeMapItems(rescanResult["failed_turns"])
	if len(failedItems) == 0 || s.Store == nil {
		return
	}
	memories, err := s.Store.ListMemories(ctx, sid, 0, 0)
	if err != nil {
		return
	}
	derived := map[int]bool{}
	for _, memory := range memories {
		derived[memory.TurnIndex] = true
	}
	still, recovered := []map[string]any{}, []int{}
	requeued := 0
	for _, item := range failedItems {
		if turn := intFromAny(item["turn_index"], 0); turn > 0 && derived[turn] {
			recovered = append(recovered, turn)
			if stringFromAny(item["state"]) == "retryable" {
				requeued++
			}
			continue
		}
		still = append(still, item)
	}
	if len(recovered) == 0 {
		return
	}
	failed := maxInt(0, intFromAny(rescanResult["failed"], 0)-len(recovered))
	rescanResult["failed_turns"] = still
	rescanResult["failed"] = failed
	rescanResult["queued"] = maxInt(0, intFromAny(rescanResult["queued"], 0)-requeued)
	rescanResult["recovered_turns"] = recovered
	if failed == 0 && rescanResult["status"] == "partial_error" {
		rescanResult["status"] = "ok"
	}
}
