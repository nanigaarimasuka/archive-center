package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/risulongmemory/archive-center-go/internal/config"
	"github.com/risulongmemory/archive-center-go/internal/dto"
	"github.com/risulongmemory/archive-center-go/internal/vector"
)

// earlySearchVectorStore answers each search from its arguments, so equal
// searches give equal results, and reports its first search.
type earlySearchVectorStore struct {
	vector.VectorStore
	mu       sync.Mutex
	searches int
	first    chan struct{}
}

func (s *earlySearchVectorStore) Search(_ context.Context, sessionID string, query []float32, limit int, filter string) ([]vector.VectorDocument, error) {
	s.mu.Lock()
	s.searches++
	if s.searches == 1 {
		close(s.first)
	}
	s.mu.Unlock()
	docs := []vector.VectorDocument{}
	for i := 0; i < 3 && i < limit; i++ {
		docs = append(docs, vector.VectorDocument{
			ID: fmt.Sprintf("doc-%g-%d-%d", query[0], len(filter)%7, i), ChatSessionID: sessionID,
			DocumentText: fmt.Sprintf("text %g %d", query[0], i), Tier: "memory",
			Similarity: float64(query[0])/10 - float64(i)/100, SimilarityAvailable: true,
			Metadata: map[string]any{"filter": filter},
		})
	}
	return docs, nil
}

// Kept recent-turn embeddings start their searches while the current input is
// still being embedded, and the recall is the one computed without them.
func TestPrepareTurnSearchesKeptEmbeddingsBeforeCurrentInput(t *testing.T) {
	oldCache := sharedQueryEmbeddingCache
	sharedQueryEmbeddingCache = newQueryEmbeddingCache(nil)
	defer func() { sharedQueryEmbeddingCache = oldCache }()

	raw := "Recall the silver shield."
	req := dto.PrepareTurnRequest{ChatSessionID: "early-search", RawUserInput: &raw,
		ClientMeta: map[string]any{"embedding": map[string]any{"provider": "custom", "endpoint": "https://controlled.invalid/v1", "api_key": "synthetic", "model": "fixture", "timeout_ms": 30000}}}
	for i := 0; i < 5; i++ {
		req.Messages = append(req.Messages, map[string]any{"role": "user", "content": fmt.Sprintf("Visit %d", i)}, map[string]any{"role": "assistant", "content": fmt.Sprintf("Room %d was inspected.", i)})
	}
	queries := prepareTurnRetrievalQueries(req, prepareTurnRecentConversationReferenceLimit(req.Settings))
	vectorFor := func(index int) string { return fmt.Sprintf("[%d,0.2,0.3]", index+1) }
	indices := map[string]int{}
	for i, q := range queries {
		indices[q.Text] = i
	}

	run := func(ctx context.Context, store *earlySearchVectorStore, waitForSearch bool) (map[string]any, bool) {
		cfg := config.Default()
		cfg.ChromaEndpoint = "http://controlled-vector.invalid"
		srv := NewServer(cfg)
		srv.Vector = store
		srv.VectorOpenError = nil
		searchedFirst := false
		oldClient := proxyHTTPClient
		proxyHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				return nil, err
			}
			index, ok := indices[extractionStringFromAny(body["input"])]
			if !ok {
				return nil, fmt.Errorf("unexpected embedding input: %v", body["input"])
			}
			if index == 0 && waitForSearch {
				select {
				case <-store.first:
					searchedFirst = true
				case <-time.After(2 * time.Second):
				}
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"embedding":` + vectorFor(index) + `}]}`))}, nil
		})}
		defer func() { proxyHTTPClient = oldClient }()
		return srv.prepareTurnVectorShadow(ctx, req, 5), searchedFirst
	}

	// Keep the recent turns' embeddings as earlier requests would have.
	embedder := NewServer(config.Default()).completeTurnExtractionConfig(req.ClientMeta).Embedder
	for i := 1; i < len(queries); i++ {
		sharedQueryEmbeddingCache.store(newQueryEmbeddingCacheKey(embedder, queries[i].Text), vectorFor(i), "fixture")
	}

	early := &earlySearchVectorStore{VectorStore: vector.NewFakeVectorStore(), first: make(chan struct{})}
	got, searchedFirst := run(withQueryEmbeddingCache(context.Background()), early, true)
	if !searchedFirst {
		t.Fatal("no search started before the current input was embedded")
	}
	plain := &earlySearchVectorStore{VectorStore: vector.NewFakeVectorStore(), first: make(chan struct{})}
	want, _ := run(context.Background(), plain, false)
	if early.searches != plain.searches {
		t.Fatalf("searches %d, want %d", early.searches, plain.searches)
	}
	for _, key := range []string{"search_results", "search_result_count", "memory_search_results", "memory_search_result_count", "query_embedding_count", "status"} {
		if a, b := mustCompactJSON(got[key]), mustCompactJSON(want[key]); a != b {
			t.Fatalf("%s differs:\n got %s\nwant %s", key, a, b)
		}
	}
	if a, b := mustCompactJSON(prepareTurnVectorMemorySearchResultMaps(got)), mustCompactJSON(prepareTurnVectorMemorySearchResultMaps(want)); a != b {
		t.Fatalf("memory hits differ:\n got %s\nwant %s", a, b)
	}
}
