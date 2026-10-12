package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/risulongmemory/archive-center-go/internal/config"
	"github.com/risulongmemory/archive-center-go/internal/store"
)

type episodeDeleteCountingStore struct {
	*adminEpisodeBackfillStore
	deletes int
}

func (f *episodeDeleteCountingStore) DeleteEpisodeSummariesInRange(ctx context.Context, sid string, fromTurn, toTurn int) (int64, error) {
	f.deletes++
	return 0, nil
}

func hierarchyFailureTestLogs(sid string, turns int) []store.ChatLog {
	logs := []store.ChatLog{}
	for turn := 1; turn <= turns; turn++ {
		logs = append(logs,
			store.ChatLog{ID: int64(turn * 2), ChatSessionID: sid, TurnIndex: turn, Role: "user", Content: "user turn"},
			store.ChatLog{ID: int64(turn*2 + 1), ChatSessionID: sid, TurnIndex: turn, Role: "assistant", Content: "assistant turn"},
		)
	}
	return logs
}

func TestEpisodeMissingMemoryTurns(t *testing.T) {
	logs := hierarchyFailureTestLogs("s", 10)
	memories := []store.Memory{{ChatSessionID: "s", TurnIndex: 1}, {ChatSessionID: "s", TurnIndex: 2}, {ChatSessionID: "s", TurnIndex: 4}, {ChatSessionID: "other", TurnIndex: 5}}
	if got := episodeMissingMemoryTurns("s", 1, 5, logs, memories); !reflect.DeepEqual(got, []int{3, 5}) {
		t.Fatalf("missing turns = %v, want [3 5]", got)
	}
	if got := episodeMissingMemoryTurns("s", 1, 5, logs, nil); len(got) != 0 {
		t.Fatalf("a session without any memory (no critic) was blocked: %v", got)
	}
}

func TestEpisodeBackfillWaitsForEveryTurnMemoryAndKeepsTheOldEpisode(t *testing.T) {
	sid := "sess-missing-memory"
	fake := &episodeDeleteCountingStore{adminEpisodeBackfillStore: &adminEpisodeBackfillStore{
		turnRecordingStore: &turnRecordingStore{},
	}}
	fake.returnEpisodeSums = []store.EpisodeSummary{{ChatSessionID: sid, FromTurn: 1, ToTurn: 5, SummaryText: "old"}}
	cfg := config.Default()
	cfg.StoreMode = config.StoreModeMariaDBAuthority
	srv := NewServer(cfg)
	srv.StoreOpenError = nil
	srv.Store = fake

	logs := hierarchyFailureTestLogs(sid, 10)
	memories := []store.Memory{}
	for turn := 1; turn <= 10; turn++ {
		if turn != 5 {
			memories = append(memories, store.Memory{ChatSessionID: sid, TurnIndex: turn})
		}
	}
	result := srv.backfillEpisodeSummariesFromChatLogs(context.Background(), sid, logs, memories, nil, 5, false, nil, true)
	if fake.deletes != 0 {
		t.Fatalf("deletes = %d, the old 1-5 episode was removed while turn 5 lacks a memory", fake.deletes)
	}
	if len(fake.savedEpisodes) != 1 || fake.savedEpisodes[0].FromTurn != 6 {
		t.Fatalf("saved episodes = %+v, want only 6-10", fake.savedEpisodes)
	}
	blocked, _ := result["blocked"].([]map[string]any)
	if len(blocked) != 1 || blocked[0]["reason"] != "blocked_missing_memory" || !reflect.DeepEqual(blocked[0]["missing_memory_turns"], []int{5}) {
		t.Fatalf("blocked = %+v, want 1-5 blocked on turn 5", blocked)
	}
}

func TestChapterLLMFailureSavesNothing(t *testing.T) {
	sid := "sess-chapter-refusal"
	fake := &adminEpisodeBackfillStore{turnRecordingStore: &turnRecordingStore{}}
	fake.returnEpisodeSums = []store.EpisodeSummary{
		{ChatSessionID: sid, FromTurn: 1, ToTurn: 5, SummaryText: "first"},
		{ChatSessionID: sid, FromTurn: 6, ToTurn: 10, SummaryText: "second"},
	}
	cfg := config.Default()
	cfg.StoreMode = config.StoreModeMariaDBAuthority
	srv := NewServer(cfg)
	srv.StoreOpenError = nil
	srv.Store = fake
	srv.RuntimeConfig.MainProvider = "openai"
	srv.RuntimeConfig.MainAPIKey = "sk-test"
	srv.RuntimeConfig.MainEndpoint = "https://api.example.com/v1"
	srv.RuntimeConfig.MainModel = "publisher"
	srv.RuntimeConfig.MainTimeoutSec = 30

	refusal, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": "I cannot fulfill this request."}}},
	})
	oldClient := proxyHTTPClient
	proxyHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(refusal)))}, nil
	})}
	defer func() { proxyHTTPClient = oldClient }()

	layer, err := srv.backfillChapterSummaries(context.Background(), sid, 1, 10, 2, nil, false, false)
	if err != nil {
		t.Fatalf("backfill error: %v", err)
	}
	if len(fake.savedChapters) != 0 {
		t.Fatalf("a refused chapter was saved: %+v", fake.savedChapters)
	}
	if intFromAny(layer["failed"], 0) != 1 || intFromAny(layer["generated"], 0) != 0 {
		t.Fatalf("layer = %+v, want one failed range", layer)
	}
	blocked, _ := layer["blocked"].([]map[string]any)
	if len(blocked) != 1 || blocked[0]["reason"] != "llm_failed" || blocked[0]["from_turn"] != 1 || blocked[0]["to_turn"] != 10 {
		t.Fatalf("blocked = %+v, want 1-10 llm_failed", blocked)
	}
}

func TestSummaryFailuresFindsTheGaps(t *testing.T) {
	sid := "sess-gaps"
	memories := []store.Memory{}
	for turn := 1; turn <= 22; turn++ {
		if turn != 13 {
			memories = append(memories, store.Memory{ChatSessionID: sid, TurnIndex: turn})
		}
	}
	fake := &adminRegeneratedArtifactStore{adminEpisodeBackfillStore: &adminEpisodeBackfillStore{
		turnRecordingStore: &turnRecordingStore{returnChatLogs: hierarchyFailureTestLogs(sid, 22), returnMemories: memories},
	}}
	// 11-15 is missing because turn 13 has no memory; 16-20 failed for
	// another reason. 21-22 is still open and not a failure.
	fake.returnEpisodeSums = []store.EpisodeSummary{
		{ChatSessionID: sid, FromTurn: 1, ToTurn: 5},
		{ChatSessionID: sid, FromTurn: 6, ToTurn: 10},
	}
	// Episodes 1-10 form a full chapter group that has no chapter.
	cfg := config.Default()
	cfg.StoreMode = config.StoreModeMariaDBAuthority
	srv := NewServer(cfg)
	srv.StoreOpenError = nil
	srv.Store = fake

	report, err := srv.summaryFailures(context.Background(), sid, summaryFailureIntervals{Episode: 5, Chapter: 2, Arc: 2, Saga: 2})
	if err != nil {
		t.Fatalf("summaryFailures: %v", err)
	}
	if !reflect.DeepEqual(report.MissingMemoryTurns, []int{13}) {
		t.Fatalf("missing memory turns = %v, want [13]", report.MissingMemoryTurns)
	}
	want := []summaryFailureRange{
		{FromTurn: 11, ToTurn: 15, Reason: "blocked_missing_memory", MissingTurns: []int{13}},
		{FromTurn: 16, ToTurn: 20, Reason: "episode_missing"},
	}
	if !reflect.DeepEqual(report.Episodes, want) {
		t.Fatalf("episodes = %+v, want %+v", report.Episodes, want)
	}
	if !reflect.DeepEqual(report.Chapters, []summaryFailureRange{{FromTurn: 1, ToTurn: 10, Reason: "chapter_missing"}}) {
		t.Fatalf("chapters = %+v, want 1-10 missing", report.Chapters)
	}
	if len(report.Arcs) != 0 || len(report.Sagas) != 0 || report.total() != 4 {
		t.Fatalf("report = %+v, want 4 failures and no arc/saga gaps", report)
	}
}

func TestRecoveredMemoryFillsTheHeldEpisode(t *testing.T) {
	sid := "sess-recovered"
	memories := []store.Memory{}
	for turn := 1; turn <= 7; turn++ {
		memories = append(memories, store.Memory{ChatSessionID: sid, TurnIndex: turn})
	}
	fake := &adminRegeneratedArtifactStore{adminEpisodeBackfillStore: &adminEpisodeBackfillStore{
		turnRecordingStore: &turnRecordingStore{returnChatLogs: hierarchyFailureTestLogs(sid, 7), returnMemories: memories},
	}}
	srv := NewServer(config.Default())
	srv.Store = fake
	srv.pluginSettings = []byte(`{"episodeIntervalTurns":3}`)

	srv.fillSummariesAfterRecoveredMemory(context.Background(), sid, 5)
	if len(fake.savedEpisodes) != 1 || fake.savedEpisodes[0].FromTurn != 4 || fake.savedEpisodes[0].ToTurn != 6 {
		t.Fatalf("saved episodes = %+v, want the 4-6 episode from the saved interval", fake.savedEpisodes)
	}
	srv.fillSummariesAfterRecoveredMemory(context.Background(), sid, 7)
	if len(fake.savedEpisodes) != 1 {
		t.Fatalf("an open range got an episode: %+v", fake.savedEpisodes)
	}
}
