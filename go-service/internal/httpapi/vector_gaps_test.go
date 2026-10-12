package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/risulongmemory/archive-center-go/internal/config"
	"github.com/risulongmemory/archive-center-go/internal/store"
	"github.com/risulongmemory/archive-center-go/internal/vector"
)

func TestVectorGapsFindOnlyMissingAndLeftoverDocuments(t *testing.T) {
	sid := "sess-gaps"
	memories := []store.Memory{
		{ID: 1, ChatSessionID: sid, TurnIndex: 1, SummaryJSON: `{"turn_summary":"kept"}`, Embedding: `[0.1]`},
		{ID: 2, ChatSessionID: sid, TurnIndex: 2, SummaryJSON: `{"turn_summary":"no vector yet"}`, Embedding: `[0.2]`},
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

// A memory row whose embedding is empty is replayed on its own, like a row
// without a vector, even when its vector document is there.
func TestVectorGapsReplayMemoryRowsWithoutEmbedding(t *testing.T) {
	sid := "sess-no-embedding"
	memories := []store.Memory{
		{ID: 1, ChatSessionID: sid, TurnIndex: 1, SummaryJSON: `{"turn_summary":"embedded"}`, Embedding: `[0.1]`},
		{ID: 2, ChatSessionID: sid, TurnIndex: 2, SummaryJSON: `{"turn_summary":"vector but no embedding"}`, Embedding: ``},
		{ID: 3, ChatSessionID: sid, TurnIndex: 3, SummaryJSON: `{"turn_summary":"empty list"}`, Embedding: `[]`},
	}
	vectors := vector.NewFakeVectorStore()
	docs := []vector.VectorDocument{}
	for _, mem := range memories {
		docs = append(docs, vector.VectorDocument{ID: memoryVectorDocumentID(sid, mem), ChatSessionID: sid, Tier: "memory", SourceTable: "memories", SourceRowID: strconv.FormatInt(mem.ID, 10)})
	}
	if err := vectors.Upsert(context.Background(), sid, docs); err != nil {
		t.Fatal(err)
	}
	srv := &Server{Cfg: config.Default(), Store: &turnRecordingStore{returnMemories: memories}, Vector: vectors}

	gaps, err := srv.adminVectorGaps(context.Background(), sid)
	if err != nil {
		t.Fatalf("adminVectorGaps: %v", err)
	}
	if gaps.MissingCount != 2 || gaps.MissingTurns[1] || !gaps.MissingTurns[2] || !gaps.MissingTurns[3] || len(gaps.OrphanIDs) != 0 {
		t.Fatalf("gaps = %d %v orphans %v, want turns 2 and 3 only", gaps.MissingCount, gaps.MissingTurns, gaps.OrphanIDs)
	}
}

// Only a model change re-embeds the whole session; rows without an embedding
// leave the repair to the gaps.
func TestReembedNeedsWholeSessionOnlyForModelChanges(t *testing.T) {
	for _, tc := range []struct {
		reasons []string
		whole   bool
	}{
		{nil, false},
		{[]string{"memory_rows_missing_embedding"}, false},
		{[]string{"embedding_model_mismatch"}, true},
		{[]string{"memory_rows_missing_embedding", "embedding_model_mismatch"}, true},
		{[]string{"vector_project_model_mismatch"}, true},
	} {
		if got := reembedNeedsWholeSession(tc.reasons); got != tc.whole {
			t.Fatalf("reembedNeedsWholeSession(%v) = %v, want %v", tc.reasons, got, tc.whole)
		}
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
