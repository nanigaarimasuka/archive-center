package httpapi

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/risulongmemory/archive-center-go/internal/config"
	"github.com/risulongmemory/archive-center-go/internal/dto"
	"github.com/risulongmemory/archive-center-go/internal/store"
)

type turnEditFakeStore struct {
	*adminEpisodeBackfillStore
	edits   []store.HistoricalTurnEdit
	deleted []string
}

func (f *turnEditFakeStore) ReviseHistoricalTurn(ctx context.Context, edit store.HistoricalTurnEdit) error {
	f.edits = append(f.edits, edit)
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
