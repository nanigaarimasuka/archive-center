package httpapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/risulongmemory/archive-center-go/internal/store"
)

func TestListFieldLooksGoFormatted(t *testing.T) {
	for raw, want := range map[string]bool{
		`["[a b c]"]`:               true,
		`["map[name:엘]"]`:           true,
		`["a real item","another"]`: false,
		`["[인용] 으로 시작하는 항목도 괜찮음"]`: false,
		`[]`:       false,
		`not json`: false,
	} {
		if got := listFieldLooksGoFormatted(raw); got != want {
			t.Fatalf("listFieldLooksGoFormatted(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestHierarchyRebuildTargetsFindBrokenAndRuleBasedSummaries(t *testing.T) {
	sid := "sess-health"
	fake := &turnEditFakeStore{adminEpisodeBackfillStore: &adminEpisodeBackfillStore{turnRecordingStore: &turnRecordingStore{}}}
	fake.chapterSummaries = []store.ChapterSummary{
		{ChatSessionID: sid, FromTurn: 1, ToTurn: 10, ChapterIndex: 1, ChapterTitle: "제1장", OpenLoopsJSON: `["[a b]"]`},
		{ChatSessionID: sid, FromTurn: 11, ToTurn: 20, ChapterIndex: 2, ChapterTitle: "Chapter 2", ResumeText: "Turns 11-20: rule text"},
		{ChatSessionID: sid, FromTurn: 21, ToTurn: 30, ChapterIndex: 3, ChapterTitle: "Chapter 3", ResumeText: "LLM resume without the marker"},
	}
	fake.arcSummaries = []store.ArcSummary{
		{ChatSessionID: sid, FromTurn: 1, ToTurn: 20, ArcIndex: 1, ArcName: "Arc 1", ArcResumeText: "LLM-written arc"},
	}
	srv := turnEditTestServer(fake)
	targets := srv.hierarchyRebuildTargets(context.Background(), sid)
	if len(targets) != 3 {
		t.Fatalf("targets = %+v, want chapters 1-10 and 11-20 and the arc over them", targets)
	}
	if targets[0].Reasons[0] != "broken_list_fields" || targets[1].Reasons[0] != "rule_based_summary" {
		t.Fatalf("chapter reasons = %+v", targets[:2])
	}
	if targets[2].Layer != "arc" || len(targets[2].Reasons) != 1 || targets[2].Reasons[0] != "contains_rebuilt_summary" {
		t.Fatalf("an LLM arc missing only its name must not count as rule-based: %+v", targets[2])
	}
}

func TestRebuildClimbsOnlyAboveWhatSucceeded(t *testing.T) {
	sid := "sess-climb"
	fake := &turnEditFakeStore{adminEpisodeBackfillStore: &adminEpisodeBackfillStore{turnRecordingStore: &turnRecordingStore{}}}
	fake.returnEpisodeSums = []store.EpisodeSummary{
		{ChatSessionID: sid, FromTurn: 1, ToTurn: 5}, {ChatSessionID: sid, FromTurn: 6, ToTurn: 10},
	}
	fake.chapterSummaries = []store.ChapterSummary{{ChatSessionID: sid, FromTurn: 1, ToTurn: 10, ChapterIndex: 1, OpenLoopsJSON: `["[x]"]`}}
	fake.arcSummaries = []store.ArcSummary{{ChatSessionID: sid, FromTurn: 1, ToTurn: 10, ArcIndex: 1}}
	srv := turnEditTestServer(fake)
	srv.RuntimeConfig.MainProvider = "openai"
	srv.RuntimeConfig.MainAPIKey = "sk-test"
	srv.RuntimeConfig.MainEndpoint = "https://api.example.com/v1"
	srv.RuntimeConfig.MainModel = "publisher"
	srv.RuntimeConfig.MainTimeoutSec = 30
	reply := `{"choices":[{"message":{"content":"{\"chapter_title\":\"New\",\"summary_text\":\"ok\",\"arc_name\":\"Arc\",\"core_conflict\":\"c\"}"}}]}`
	oldClient := proxyHTTPClient
	proxyHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(reply))}, nil
	})}
	defer func() { proxyHTTPClient = oldClient }()

	result := srv.rebuildFlaggedHierarchy(context.Background(), sid)
	if len(fake.savedChapters) != 1 || len(fake.savedArcs) != 1 {
		t.Fatalf("saved chapters=%d arcs=%d, want the chapter and the arc above it: %+v", len(fake.savedChapters), len(fake.savedArcs), result)
	}

	proxyHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"I cannot help with that."}}]}`))}, nil
	})}
	failed := srv.rebuildHierarchyFrom(context.Background(), sid, "chapter", map[int]bool{1: true})
	if failed["arc"] != nil || len(fake.savedArcs) != 1 {
		t.Fatalf("a failed chapter still rebuilt the arc above it: %+v", failed)
	}
}

func TestFlaggedArcIsRebuiltOnceWhenItsChapterClimbs(t *testing.T) {
	sid := "sess-once"
	fake := &turnEditFakeStore{adminEpisodeBackfillStore: &adminEpisodeBackfillStore{turnRecordingStore: &turnRecordingStore{}}}
	fake.returnEpisodeSums = []store.EpisodeSummary{
		{ChatSessionID: sid, FromTurn: 1, ToTurn: 5}, {ChatSessionID: sid, FromTurn: 6, ToTurn: 10},
	}
	fake.chapterSummaries = []store.ChapterSummary{{ChatSessionID: sid, FromTurn: 1, ToTurn: 10, ChapterIndex: 1, OpenLoopsJSON: `["[x]"]`}}
	fake.arcSummaries = []store.ArcSummary{{ChatSessionID: sid, FromTurn: 1, ToTurn: 10, ArcIndex: 1, KeyTurningPointsJSON: `["[y]"]`}}
	srv := turnEditTestServer(fake)
	srv.RuntimeConfig.MainProvider = "openai"
	srv.RuntimeConfig.MainAPIKey = "sk-test"
	srv.RuntimeConfig.MainEndpoint = "https://api.example.com/v1"
	srv.RuntimeConfig.MainModel = "publisher"
	srv.RuntimeConfig.MainTimeoutSec = 30
	calls := 0
	reply := `{"choices":[{"message":{"content":"{\"chapter_title\":\"New\",\"summary_text\":\"ok\",\"arc_name\":\"Arc\",\"core_conflict\":\"c\"}"}}]}`
	oldClient := proxyHTTPClient
	proxyHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(reply))}, nil
	})}
	defer func() { proxyHTTPClient = oldClient }()

	srv.rebuildFlaggedHierarchy(context.Background(), sid)
	if calls != 2 || len(fake.savedArcs) != 1 {
		t.Fatalf("LLM calls=%d saved arcs=%d, want one chapter and one arc", calls, len(fake.savedArcs))
	}
}

func TestArcFromTheOldPromptIsFlagged(t *testing.T) {
	old := store.ArcSummary{FromTurn: 1, ToTurn: 240, ArcIndex: 1, ArcName: "Arc 1", CoreConflict: "open_loop: 남은 일", ArcResumeText: "LLM resume"}
	if reasons := arcRebuildReasons(old); len(reasons) != 1 || reasons[0] != "rule_based_fields" {
		t.Fatalf("reasons = %v, want rule_based_fields", reasons)
	}
	named := old
	named.CoreConflict = "엘과 제압반의 주도권 다툼"
	if reasons := arcRebuildReasons(named); len(reasons) != 0 {
		t.Fatalf("an arc with a written core conflict was flagged: %v", reasons)
	}
}
