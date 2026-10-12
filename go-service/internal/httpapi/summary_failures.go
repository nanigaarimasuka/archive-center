package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/risulongmemory/archive-center-go/internal/store"
)

// Failed summaries leave nothing behind, so they are found by looking for the
// gaps: turns without a memory, closed turn ranges without an episode, and
// full groups of episodes, chapters, or arcs without the summary above them.
// Session normalize fills the same gaps, which is how the user retries them.

type summaryFailureRange struct {
	FromTurn     int    `json:"from_turn"`
	ToTurn       int    `json:"to_turn"`
	Reason       string `json:"reason"`
	MissingTurns []int  `json:"missing_memory_turns,omitempty"`
}

type summaryFailureReport struct {
	MissingMemoryTurns []int                 `json:"missing_memory_turns"`
	Episodes           []summaryFailureRange `json:"episodes"`
	Chapters           []summaryFailureRange `json:"chapters"`
	Arcs               []summaryFailureRange `json:"arcs"`
	Sagas              []summaryFailureRange `json:"sagas"`
	// Rebuild lists stored summaries session normalize would make again.
	Rebuild []hierarchyRebuildTarget `json:"rebuild"`
}

func (r summaryFailureReport) total() int {
	return len(r.MissingMemoryTurns) + len(r.Episodes) + len(r.Chapters) + len(r.Arcs) + len(r.Sagas)
}

type summaryFailureIntervals struct {
	Episode int
	Chapter int
	Arc     int
	Saga    int
}

func (s *Server) handleSummaryFailures(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	if sid == "" {
		writeError(w, http.StatusBadRequest, "missing_param", "session_id is required")
		return
	}
	if s.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "error", "code": "store_unavailable"})
		return
	}
	query := r.URL.Query()
	queryInt := func(key string) int {
		n, _ := strconv.Atoi(strings.TrimSpace(query.Get(key)))
		if n < 0 {
			return 0
		}
		return n
	}
	intervals := summaryFailureIntervals{
		Episode: normalizedEpisodeInterval(queryInt("episode_interval_turns")),
		Chapter: queryInt("chapter_interval_episodes"),
		Arc:     queryInt("arc_interval_chapters"),
		Saga:    queryInt("saga_interval_arcs"),
	}
	report, err := s.summaryFailures(r.Context(), sid, intervals)
	if err != nil {
		writeInternalError(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":          "ok",
		"chat_session_id": sid,
		"failure_count":   report.total(),
		"rebuild_count":   len(report.Rebuild),
		"failures":        report,
	})
}

func (s *Server) summaryFailures(ctx context.Context, sid string, intervals summaryFailureIntervals) (summaryFailureReport, error) {
	report := summaryFailureReport{
		Rebuild:            s.hierarchyRebuildTargets(ctx, sid),
		MissingMemoryTurns: []int{},
		Episodes:           []summaryFailureRange{},
		Chapters:           []summaryFailureRange{},
		Arcs:               []summaryFailureRange{},
		Sagas:              []summaryFailureRange{},
	}
	ignorable := func(err error) bool {
		return err == nil || errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrNotEnabled)
	}
	logs, err := s.Store.ListChatLogs(ctx, sid, 0, 0)
	if !ignorable(err) {
		return report, err
	}
	memories, err := s.Store.ListMemories(ctx, sid, 0, 0)
	if !ignorable(err) {
		return report, err
	}
	minTurn, maxTurn := chatLogTurnBounds(sid, logs)
	if minTurn <= 0 || maxTurn <= 0 {
		return report, nil
	}
	report.MissingMemoryTurns = append(report.MissingMemoryTurns, episodeMissingMemoryTurns(sid, minTurn, maxTurn, logs, memories)...)

	episodes, err := s.Store.ListEpisodeSummaries(ctx, sid, 0, 0, 0)
	if !ignorable(err) {
		return report, err
	}
	episodeRanges := map[[2]int]bool{}
	for _, item := range episodes {
		episodeRanges[[2]int{item.FromTurn, item.ToTurn}] = true
	}
	interval := intervals.Episode
	start := minTurn
	if start > 1 {
		start = ((start-1)/interval)*interval + 1
	}
	for fromTurn := start; fromTurn+interval-1 <= maxTurn; fromTurn += interval {
		toTurn := fromTurn + interval - 1
		if episodeRanges[[2]int{fromTurn, toTurn}] {
			continue
		}
		if len(filterChatLogsForTurnRange(logs, fromTurn, toTurn, 0)) == 0 {
			continue
		}
		item := summaryFailureRange{FromTurn: fromTurn, ToTurn: toTurn, Reason: "episode_missing"}
		if missing := episodeMissingMemoryTurns(sid, fromTurn, toTurn, logs, memories); len(missing) > 0 {
			item.Reason = "blocked_missing_memory"
			item.MissingTurns = missing
		}
		report.Episodes = append(report.Episodes, item)
	}

	if intervals.Chapter <= 0 {
		return report, nil
	}
	chapters := []store.ChapterSummary{}
	if chapterStore, ok := s.Store.(store.ChapterSummaryStore); ok {
		chapters, err = chapterStore.SearchChapterSummaries(ctx, sid, "", 0, 0, 0)
		if !ignorable(err) {
			return report, err
		}
	}
	report.Chapters = missingParentRanges(summaryChildRanges(episodes, minTurn, maxTurn), summaryParentRanges(chapters), intervals.Chapter, "chapter_missing")

	if intervals.Arc <= 0 {
		return report, nil
	}
	arcs := []store.ArcSummary{}
	if arcStore, ok := s.Store.(store.ArcSummaryStore); ok {
		arcs, err = arcStore.SearchArcSummaries(ctx, sid, "", 0, 0, 0)
		if !ignorable(err) {
			return report, err
		}
	}
	report.Arcs = missingParentRanges(summaryChildRanges(chapters, minTurn, maxTurn), summaryParentRanges(arcs), intervals.Arc, "arc_missing")

	if intervals.Saga <= 0 {
		return report, nil
	}
	sagas := []store.SagaDigest{}
	if sagaStore, ok := s.Store.(store.SagaDigestStore); ok {
		sagas, err = sagaStore.SearchSagaDigests(ctx, sid, "", 0, 0, 0)
		if !ignorable(err) {
			return report, err
		}
	}
	report.Sagas = missingParentRanges(summaryChildRanges(arcs, minTurn, maxTurn), summaryParentRanges(sagas), intervals.Saga, "saga_missing")
	return report, nil
}

type summaryRanged interface {
	store.EpisodeSummary | store.ChapterSummary | store.ArcSummary | store.SagaDigest
}

func summaryRangeOf[T summaryRanged](item T) (int, int) {
	switch v := any(item).(type) {
	case store.EpisodeSummary:
		return v.FromTurn, v.ToTurn
	case store.ChapterSummary:
		return v.FromTurn, v.ToTurn
	case store.ArcSummary:
		return v.FromTurn, v.ToTurn
	case store.SagaDigest:
		return v.FromTurn, v.ToTurn
	}
	return 0, 0
}

// summaryChildRanges mirrors the backfill: distinct, sorted child ranges
// inside the session's turns, grouped later in fixed-size runs.
func summaryChildRanges[T summaryRanged](items []T, minTurn, maxTurn int) []turnRange {
	out := []turnRange{}
	seen := map[string]bool{}
	for _, item := range items {
		fromTurn, toTurn := summaryRangeOf(item)
		if fromTurn < minTurn || toTurn > maxTurn || fromTurn <= 0 || toTurn < fromTurn {
			continue
		}
		key := fmt.Sprintf("%d:%d", fromTurn, toTurn)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, turnRange{fromTurn: fromTurn, toTurn: toTurn})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].fromTurn == out[j].fromTurn {
			return out[i].toTurn < out[j].toTurn
		}
		return out[i].fromTurn < out[j].fromTurn
	})
	return out
}

func summaryParentRanges[T summaryRanged](items []T) map[[2]int]bool {
	out := map[[2]int]bool{}
	for _, item := range items {
		fromTurn, toTurn := summaryRangeOf(item)
		out[[2]int{fromTurn, toTurn}] = true
	}
	return out
}

func missingParentRanges(children []turnRange, parents map[[2]int]bool, interval int, reason string) []summaryFailureRange {
	out := []summaryFailureRange{}
	for start := 0; start+interval <= len(children); start += interval {
		group := children[start : start+interval]
		fromTurn, toTurn := group[0].fromTurn, group[len(group)-1].toTurn
		if !turnRangesCover(group, fromTurn, toTurn) || parents[[2]int{fromTurn, toTurn}] {
			continue
		}
		out = append(out, summaryFailureRange{FromTurn: fromTurn, ToTurn: toTurn, Reason: reason})
	}
	return out
}

func blockedRangesWithReason(result map[string]any, reason string) []map[string]any {
	out := []map[string]any{}
	blocked, _ := result["blocked"].([]map[string]any)
	for _, item := range blocked {
		if item["reason"] == reason {
			out = append(out, item)
		}
	}
	return out
}

// hierarchyLLMFailureCount counts chapter, arc, and saga ranges whose LLM
// summary failed during a hierarchy checkpoint.
func hierarchyLLMFailureCount(result map[string]any) int {
	count := 0
	for _, key := range []string{"chapter", "arc", "saga"} {
		if layer, ok := result[key].(map[string]any); ok {
			count += intFromAny(layer["failed"], 0)
		}
	}
	return count
}

// pluginSummaryIntervals reads the summary intervals the plugin saved last,
// for work that runs without a plugin request, such as the critic
// reprocessing worker. The defaults match the plugin's.
func (s *Server) pluginSummaryIntervals() map[string]any {
	meta := map[string]any{
		"episode_interval_turns":    5,
		"chapter_interval_episodes": 12,
		"arc_interval_chapters":     4,
		"saga_interval_arcs":        4,
	}
	s.pluginSettingsMu.Lock()
	raw := s.pluginSettings
	s.pluginSettingsMu.Unlock()
	saved := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &saved)
	}
	for key, pluginKey := range map[string]string{
		"episode_interval_turns":    "episodeIntervalTurns",
		"chapter_interval_episodes": "chapterIntervalEpisodes",
		"arc_interval_chapters":     "arcIntervalChapters",
		"saga_interval_arcs":        "sagaIntervalArcs",
	} {
		if value := intFromAny(saved[pluginKey], 0); value > 0 {
			meta[key] = value
		}
	}
	return meta
}

// fillSummariesAfterRecoveredMemory builds what waited on a turn's memory
// once a retried critic call has saved it: the episode over the turn and,
// above it, any chapter, arc, or saga that is now complete. Ranges that are
// still open or still miss another memory stay as they are.
func (s *Server) fillSummariesAfterRecoveredMemory(ctx context.Context, sid string, turnIndex int) map[string]any {
	result := map[string]any{"status": "skipped", "turn_index": turnIndex}
	if s == nil || s.Store == nil || turnIndex <= 0 {
		result["reason"] = "store_unavailable"
		return result
	}
	logs, err := s.Store.ListChatLogs(ctx, sid, 0, 0)
	if err != nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrNotEnabled) {
		result["status"], result["error"] = "partial_error", err.Error()
		return result
	}
	memories, _ := s.Store.ListMemories(ctx, sid, 0, 0)
	evidence, _ := s.Store.ListEvidence(ctx, sid)
	meta := s.pluginSummaryIntervals()
	targets := map[int]bool{turnIndex: true}
	interval := normalizedEpisodeInterval(intFromAny(meta["episode_interval_turns"], 0))
	result["episode"] = s.backfillEpisodeSummariesFromChatLogs(ctx, sid, logs, memories, evidence, interval, false, targets, false)
	result["hierarchy"] = s.backfillHierarchySummaries(ctx, sid, logs, targets, meta, false)
	result["status"] = "ok"
	return result
}
