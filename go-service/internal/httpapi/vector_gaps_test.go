package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/risulongmemory/archive-center-go/internal/config"
	"github.com/risulongmemory/archive-center-go/internal/store"
	"github.com/risulongmemory/archive-center-go/internal/vector"
)

func TestVectorGapsFindOnlyMissingAndLeftoverDocuments(t *testing.T) {
	sid := "sess-gaps"
	memories := []store.Memory{
		{ID: 1, ChatSessionID: sid, TurnIndex: 1, SummaryJSON: `{"turn_summary":"kept"}`},
		{ID: 2, ChatSessionID: sid, TurnIndex: 2, SummaryJSON: `{"turn_summary":"no vector yet"}`},
	}
	st := &turnRecordingStore{returnMemories: memories}
	vectors := vector.NewFakeVectorStore()
	if err := vectors.Upsert(context.Background(), sid, []vector.VectorDocument{
		{ID: memoryVectorDocumentID(sid, memories[0]), ChatSessionID: sid, Tier: "memory", SourceTable: "memories", SourceRowID: "1"},
		{ID: "memory:" + sid + ":99", ChatSessionID: sid, Tier: "memory", SourceTable: "memories", SourceRowID: "99"},
		{ID: "chapter:" + sid + ":5", ChatSessionID: sid, Tier: "chapter"},
	}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{Cfg: config.Default(), Store: st, Vector: vectors}

	gaps, err := srv.adminVectorGaps(context.Background(), sid)
	if err != nil {
		t.Fatalf("adminVectorGaps: %v", err)
	}
	if gaps.MissingCount != 1 || !gaps.MissingTurns[2] || gaps.MissingTurns[1] {
		t.Fatalf("missing = %d %v, want only turn 2", gaps.MissingCount, gaps.MissingTurns)
	}
	if !reflect.DeepEqual(gaps.OrphanIDs, []string{"memory:" + sid + ":99"}) {
		t.Fatalf("orphans = %v, want only the memory vector without a row (not the chapter)", gaps.OrphanIDs)
	}
	kept := gaps.onlyGapCandidates([]adminCanonicalMemoryReplayCandidate{
		{Source: &store.MemorySourceRevision{TurnIndex: 1, SourceRevision: "r1"}},
		{Source: &store.MemorySourceRevision{TurnIndex: 2, SourceRevision: "r2"}},
	})
	if len(kept) != 1 || kept[0].Source.TurnIndex != 2 {
		t.Fatalf("replays = %+v, want only turn 2", kept)
	}
}

func TestSameEmbeddingModelIgnoresCaseAndOpenRouterVariant(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		same bool
	}{
		{"qwen/qwen3-embedding-8b:floor", "Qwen/Qwen3-Embedding-8B", true},
		{"qwen/qwen3-embedding-8b:nitro", "qwen/qwen3-embedding-8b:floor", true},
		{"Qwen/Qwen3-Embedding-8B:nitro", "Qwen/Qwen3-Embedding-8B", true},
		{" text-embedding-3-small ", "TEXT-EMBEDDING-3-SMALL", true},
		{"qwen3-embedding:8b", "qwen3-embedding:4b", false},
		{"qwen3-embedding:8b", "qwen3-embedding", false},
		{"qwen/qwen3-embedding-8b", "qwen/qwen3-embedding-4b", false},
	} {
		if got := sameEmbeddingModel(tc.a, tc.b); got != tc.same {
			t.Fatalf("sameEmbeddingModel(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.same)
		}
	}
}

// The provider answers with the model's own name, so rows embedded under an
// OpenRouter variant id are not a model change.
func TestAdminReindexIntegrityReportTreatsOpenRouterVariantAsSameModel(t *testing.T) {
	cfg := config.Default()
	cfg.StoreMode = config.StoreModeMariaDBShadow
	cfg.ChromaEndpoint = "http://127.0.0.1:8000"
	cfg.EmbedderModel = "qwen/qwen3-embedding-8b:floor"
	srv := NewServer(cfg)
	srv.Store = &memoryFakeStore{
		memories: []store.Memory{
			{ID: 1, ChatSessionID: "sess-variant", TurnIndex: 1, SummaryJSON: `{"summary":"Embedded by name"}`, Embedding: `[0.1]`, EmbeddingModel: "Qwen/Qwen3-Embedding-8B"},
			{ID: 2, ChatSessionID: "sess-variant", TurnIndex: 2, SummaryJSON: `{"summary":"Embedded by id"}`, Embedding: `[0.2]`, EmbeddingModel: "qwen/qwen3-embedding-8b:floor"},
		},
	}
	srv.StoreOpenError = nil
	srv.Vector = &fakeVectorStore{
		healthSnapshot: vector.HealthSnapshot{Status: "ok", TotalCount: 2, ProjectModel: "Qwen/Qwen3-Embedding-8B", ModelReady: true},
		countResult:    2,
	}
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/admin/reindex", strings.NewReader(`{"chat_session_id":"sess-variant","dry_run":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	integrity, _ := resp["integrity_report"].(map[string]any)
	if integrity["embedding_model_mismatch_count"] != float64(0) || integrity["reembed_recommended"] != false {
		t.Fatalf("variant id counted as another model: %#v", integrity)
	}
}
