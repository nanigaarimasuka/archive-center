package httpapi

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/risulongmemory/archive-center-go/internal/config"
	"github.com/risulongmemory/archive-center-go/internal/dto"
	"github.com/risulongmemory/archive-center-go/internal/store"
)

type turnEditFakeStore struct {
	*adminEpisodeBackfillStore
	edits      []store.HistoricalTurnEdit
	deleted    []string
	review     map[int][]store.HistoricalTurnReviewItem
	logs       []store.ChatLog
	replayFrom []int
}

func (f *turnEditFakeStore) ReviseHistoricalTurn(ctx context.Context, edit store.HistoricalTurnEdit) ([]store.HistoricalTurnReviewItem, error) {
	f.edits = append(f.edits, edit)
	return f.review[edit.TurnIndex], nil
}

func (f *turnEditFakeStore) ListChatLogs(ctx context.Context, sid string, fromTurn, toTurn int) ([]store.ChatLog, error) {
	return f.logs, nil
}

func (f *turnEditFakeStore) ClearDerivedFromTurn(ctx context.Context, sid string, fromTurn int, now time.Time) error {
	f.replayFrom = append(f.replayFrom, fromTurn)
	return nil
}

func (f *turnEditFakeStore) DeleteHierarchySummaryRange(ctx context.Context, layer, sid string, fromTurn, toTurn int) (int64, error) {
	f.deleted = append(f.deleted, layer)
	if layer == "chapter" {
		kept := []store.ChapterSummary{}
		for _, item := range f.chapterSummaries {
			if item.FromTurn != fromTurn || item.ToTurn != toTurn {
				kept = append(kept, item)
			}
		}
		f.chapterSummaries = kept
	}
	return 1, nil
}

func turnEditTestServer(fake store.Store) *Server {
	cfg := config.Default()
	cfg.StoreMode = config.StoreModeMariaDBAuthority
	srv := NewServer(cfg)
	srv.StoreOpenError = nil
	srv.Store = fake
	return srv
}

func TestNormalizeAppliesOnlyAllowedConflictingTurnEdits(t *testing.T) {
	fake := &turnEditFakeStore{adminEpisodeBackfillStore: &adminEpisodeBackfillStore{turnRecordingStore: &turnRecordingStore{}}}
	fake.logs = []store.ChatLog{{TurnIndex: 9}, {TurnIndex: 10}, {TurnIndex: 11}, {TurnIndex: 11}}
	shared := store.HistoricalTurnReviewItem{Kind: "pending_thread", ID: 3, Label: "lunch tomorrow", TurnIndex: 10, OtherTurn: 14, Reason: "changed_later"}
	fake.review = map[int][]store.HistoricalTurnReviewItem{10: {shared}}
	srv := turnEditTestServer(fake)
	entries := []dto.ChatLogRepairEntryRequest{
		{TurnIndex: 10, UserContent: strPtr("new user"), AssistantContent: strPtr("new reply")},
		{TurnIndex: 11, UserContent: strPtr("u"), AssistantContent: strPtr("a")},
		{TurnIndex: 12, AssistantContent: strPtr("only reply")},
	}
	req := adminSessionNormalizeRequest{ApplyTurnEdits: []int{10, 12, 13}}
	result := srv.applyNormalizeTurnEdits(context.Background(), "sess", req, entries, []int{10, 11, 12})

	if !reflect.DeepEqual(result["edited_turns"], []int{10}) {
		t.Fatalf("edited = %v, want [10]: %+v", result["edited_turns"], result)
	}
	if len(fake.edits) != 1 || fake.edits[0].UserContent != "new user" || fake.edits[0].AssistantContent != "new reply" {
		t.Fatalf("edits = %+v", fake.edits)
	}
	if got := result["review_items"]; !reflect.DeepEqual(got, []store.HistoricalTurnReviewItem{shared}) {
		t.Fatalf("review_items = %+v, want the record turn 14 changed", got)
	}
	if got := result["replay_candidate"]; !reflect.DeepEqual(got, map[string]any{"from_turn": 10, "last_turn": 11, "turns": 2}) {
		t.Fatalf("replay_candidate = %+v", got)
	}
	reasons := map[int]string{}
	for _, item := range result["skipped"].([]map[string]any) {
		reasons[item["turn_index"].(int)] = item["reason"].(string)
	}
	if reasons[12] != "edit_needs_both_messages" || reasons[13] != "raw_not_in_conflict" {
		t.Fatalf("skipped = %+v", reasons)
	}

	none := srv.applyNormalizeTurnEdits(context.Background(), "sess", adminSessionNormalizeRequest{}, entries, []int{10, 11})
	if none["reason"] != "no_turn_edits_requested" || len(fake.edits) != 1 {
		t.Fatalf("a conflict was edited without the user's go-ahead: %+v", none)
	}
}

func TestEditedTurnRebuildsItsChapterOnlyAfterSuccess(t *testing.T) {
	sid := "sess-edit"
	fake := &turnEditFakeStore{adminEpisodeBackfillStore: &adminEpisodeBackfillStore{turnRecordingStore: &turnRecordingStore{}}}
	fake.returnEpisodeSums = []store.EpisodeSummary{
		{ChatSessionID: sid, FromTurn: 1, ToTurn: 5, SummaryText: "first"},
		{ChatSessionID: sid, FromTurn: 6, ToTurn: 10, SummaryText: "second"},
		{ChatSessionID: sid, FromTurn: 11, ToTurn: 15, SummaryText: "third"},
		{ChatSessionID: sid, FromTurn: 16, ToTurn: 20, SummaryText: "fourth"},
	}
	fake.chapterSummaries = []store.ChapterSummary{
		{ChatSessionID: sid, FromTurn: 1, ToTurn: 10, ChapterIndex: 1, SummaryText: "old one"},
		{ChatSessionID: sid, FromTurn: 11, ToTurn: 20, ChapterIndex: 2, SummaryText: "old two"},
	}
	srv := turnEditTestServer(fake)
	srv.RuntimeConfig.MainProvider = "openai"
	srv.RuntimeConfig.MainAPIKey = "sk-test"
	srv.RuntimeConfig.MainEndpoint = "https://api.example.com/v1"
	srv.RuntimeConfig.MainModel = "publisher"
	srv.RuntimeConfig.MainTimeoutSec = 30

	reply := `{"choices":[{"message":{"content":"{\"chapter_title\":\"New\",\"summary_text\":\"rewritten\"}"}}]}`
	oldClient := proxyHTTPClient
	proxyHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(reply))}, nil
	})}
	defer func() { proxyHTTPClient = oldClient }()

	layer := srv.regenerateChaptersForTurns(context.Background(), sid, map[int]bool{7: true})
	if intFromAny(layer["generated"], 0) != 1 || len(fake.savedChapters) != 1 {
		t.Fatalf("layer = %+v, saved = %+v", layer, fake.savedChapters)
	}
	if got := fake.savedChapters[0]; got.FromTurn != 1 || got.ToTurn != 10 || got.SummaryText != "rewritten" || got.ChapterIndex != 1 {
		t.Fatalf("saved chapter = %+v", got)
	}
	if len(fake.chapterSummaries) != 1 || fake.chapterSummaries[0].FromTurn != 11 {
		t.Fatalf("chapters left = %+v, want only the untouched 11-20", fake.chapterSummaries)
	}

	proxyHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"I cannot help with that."}}]}`))}, nil
	})}
	failed := srv.regenerateChaptersForTurns(context.Background(), sid, map[int]bool{12: true})
	if intFromAny(failed["failed"], 0) != 1 || len(fake.chapterSummaries) != 1 || fake.chapterSummaries[0].SummaryText != "old two" {
		t.Fatalf("a failed rebuild removed the old chapter: %+v / %+v", failed, fake.chapterSummaries)
	}
}

func TestReplayClearsFromTheTurnAndRescansEveryLaterTurn(t *testing.T) {
	fake := &turnEditFakeStore{adminEpisodeBackfillStore: &adminEpisodeBackfillStore{turnRecordingStore: &turnRecordingStore{}}}
	for _, turn := range []int{3, 4, 5, 6} {
		fake.logs = append(fake.logs, store.ChatLog{TurnIndex: turn, Role: "user"}, store.ChatLog{TurnIndex: turn, Role: "assistant"})
	}
	srv := turnEditTestServer(fake)

	result, turns, err := srv.clearDerivedForReplay(context.Background(), "sess", adminSessionNormalizeRequest{ReplayFromTurn: 4})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(turns, []int{4, 5, 6}) || !reflect.DeepEqual(fake.replayFrom, []int{4}) {
		t.Fatalf("turns = %v, cleared from %v", turns, fake.replayFrom)
	}
	if result["status"] != "ok" || result["turns"] != 3 || result["last_turn"] != 6 {
		t.Fatalf("result = %+v", result)
	}

	dry, turns, _ := srv.clearDerivedForReplay(context.Background(), "sess", adminSessionNormalizeRequest{ReplayFromTurn: 5, DryRun: true})
	if dry["status"] != "dry_run" || len(turns) != 2 || len(fake.replayFrom) != 1 {
		t.Fatalf("a dry run cleared records: %+v, cleared from %v", dry, fake.replayFrom)
	}
	skipped, turns, _ := srv.clearDerivedForReplay(context.Background(), "sess", adminSessionNormalizeRequest{})
	if skipped["reason"] != "not_requested" || turns != nil || len(fake.replayFrom) != 1 {
		t.Fatalf("replay ran unasked: %+v", skipped)
	}
}

func TestTurnsBeforeKeepsEarlierEditsOnly(t *testing.T) {
	if got := turnsBefore([]int{2, 5, 9}, 5); !reflect.DeepEqual(got, []int{2}) {
		t.Fatalf("turnsBefore = %v", got)
	}
	if got := turnsBefore([]int{2, 5}, 0); !reflect.DeepEqual(got, []int{2, 5}) {
		t.Fatalf("turnsBefore without a limit = %v", got)
	}
}

func TestReplayRetriesAFailedTurnBeforeTheNextOne(t *testing.T) {
	old := replayRetryDelays
	replayRetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { replayRetryDelays = old }()

	inOrder := &adminRescanInOrder{}
	calls := 0
	got := inOrder.retry(context.Background(), 37, func() acceptedSourceDerivationResult {
		calls++
		if calls == 1 {
			return acceptedSourceDerivationResult{State: "retryable", Failure: "CRITIC_PROVIDER_TIMEOUT"}
		}
		return acceptedSourceDerivationResult{State: "completed"}
	})
	if got.State != "completed" || calls != 2 {
		t.Fatalf("state = %q after %d calls, want completed after 2", got.State, calls)
	}
	if !reflect.DeepEqual(inOrder.Retried, []map[string]any{{"turn_index": 37, "retries": 1, "state": "completed"}}) {
		t.Fatalf("retried = %+v", inOrder.Retried)
	}
	if inOrder.stop(nil) {
		t.Fatal("a turn that succeeded on retry stopped the replay")
	}

	calls = 0
	got = inOrder.retry(context.Background(), 52, func() acceptedSourceDerivationResult {
		calls++
		return acceptedSourceDerivationResult{State: "retryable"}
	})
	if got.State != "retryable" || calls != 3 {
		t.Fatalf("state = %q after %d calls, want retryable after 3", got.State, calls)
	}
	if inOrder.stop([]map[string]any{{"turn_index": 50, "reason": "canonical_raw_pair_incomplete"}}) {
		t.Fatal("a failure a retry cannot fix stopped the rescan")
	}
	if !inOrder.stop([]map[string]any{{"turn_index": 50, "reason": "canonical_raw_pair_incomplete"}, {"turn_index": 52, "state": "retryable"}}) || inOrder.StoppedAt != 52 {
		t.Fatalf("rescan went on past a failed turn: stopped at %d", inOrder.StoppedAt)
	}

	var plain *adminRescanInOrder
	calls = 0
	plain.retry(context.Background(), 1, func() acceptedSourceDerivationResult {
		calls++
		return acceptedSourceDerivationResult{State: "retryable"}
	})
	if calls != 1 || plain.stop([]map[string]any{{"turn_index": 1, "state": "retryable"}}) {
		t.Fatalf("an ordinary rescan retried (%d calls) or stopped", calls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	replayRetryDelays = []time.Duration{time.Hour}
	calls = 0
	(&adminRescanInOrder{}).retry(ctx, 9, func() acceptedSourceDerivationResult {
		calls++
		return acceptedSourceDerivationResult{State: "retryable"}
	})
	if calls != 1 {
		t.Fatalf("a cancelled replay kept retrying: %d calls", calls)
	}
}

func TestNormalizeCountsWorkerRetriedTurnsAsRecovered(t *testing.T) {
	fake := &turnEditFakeStore{adminEpisodeBackfillStore: &adminEpisodeBackfillStore{turnRecordingStore: &turnRecordingStore{}}}
	fake.returnMemories = []store.Memory{{ChatSessionID: "sess", TurnIndex: 181}}
	srv := turnEditTestServer(fake)
	rescan := map[string]any{
		"status": "partial_error", "failed": 2, "queued": 2,
		"failed_turns": []map[string]any{
			{"turn_index": 181, "state": "retryable"},
			{"turn_index": 190, "state": "retryable"},
		},
	}
	srv.settleNormalizeRescan(context.Background(), "sess", rescan, nil, nil, nil)
	if rescan["failed"] != 1 || rescan["queued"] != 1 || rescan["status"] != "partial_error" {
		t.Fatalf("rescan = %+v", rescan)
	}
	if !reflect.DeepEqual(rescan["recovered_turns"], []int{181}) || len(rescan["failed_turns"].([]map[string]any)) != 1 {
		t.Fatalf("recovered = %v, failed = %v", rescan["recovered_turns"], rescan["failed_turns"])
	}

	fake.returnMemories = append(fake.returnMemories, store.Memory{ChatSessionID: "sess", TurnIndex: 190})
	srv.settleNormalizeRescan(context.Background(), "sess", rescan, nil, nil, nil)
	if rescan["failed"] != 0 || rescan["status"] != "ok" {
		t.Fatalf("all failures recovered but rescan = %+v", rescan)
	}
}

func TestStoppedReplayTellsWhereToResume(t *testing.T) {
	fake := &turnEditFakeStore{adminEpisodeBackfillStore: &adminEpisodeBackfillStore{turnRecordingStore: &turnRecordingStore{}}}
	srv := turnEditTestServer(fake)
	replay := map[string]any{"status": "ok", "from_turn": 30}
	rescan := map[string]any{"failed": 1, "failed_turns": []map[string]any{{"turn_index": 33, "state": "retryable"}}}
	inOrder := &adminRescanInOrder{}
	srv.settleNormalizeRescan(context.Background(), "sess", rescan, replay, inOrder, []int{30, 31, 32, 33, 34, 35})
	if replay["status"] != "stopped" || replay["stopped_at_turn"] != 33 || replay["remaining_turns"] != 3 {
		t.Fatalf("replay = %+v", replay)
	}
	if rescan["stopped_at_turn"] != 33 || rescan["remaining_turns"] != 3 {
		t.Fatalf("rescan = %+v", rescan)
	}

	// Without a replay, normalize resumes by running again.
	plain := map[string]any{"candidate_count": 10, "succeeded": 4, "skipped": 1, "failed": 1,
		"failed_turns": []map[string]any{{"turn_index": 181, "state": "retryable"}}}
	srv.settleNormalizeRescan(context.Background(), "sess", plain, map[string]any{"status": "skipped"}, &adminRescanInOrder{Retried: []map[string]any{{"turn_index": 170, "retries": 1, "state": "completed"}}}, nil)
	if plain["stopped_at_turn"] != 181 || plain["remaining_turns"] != 5 || len(plain["retried_turns"].([]map[string]any)) != 1 {
		t.Fatalf("plain normalize = %+v", plain)
	}
	if normalizeInOrder(true) != nil || normalizeInOrder(false) == nil {
		t.Fatal("a dry run must not run in order; a real normalize must")
	}
}

// withoutInOrderRetries makes normalize leave a failed turn to the
// reprocessing queue at once, as it did before it retried on the spot.
func withoutInOrderRetries(t *testing.T) {
	t.Helper()
	old := replayRetryDelays
	replayRetryDelays = nil
	t.Cleanup(func() { replayRetryDelays = old })
}
