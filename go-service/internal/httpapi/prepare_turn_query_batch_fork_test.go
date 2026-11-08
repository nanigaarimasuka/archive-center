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

	"github.com/risulongmemory/archive-center-go/internal/config"
	"github.com/risulongmemory/archive-center-go/internal/dto"
)

// Upstream recall tests count one request per query; batching is on only in
// the tests that ask for it.
func init() { prepareTurnQueryBatching = false }

func withQueryBatching(t *testing.T) {
	t.Helper()
	prepareTurnQueryBatching = true
	t.Cleanup(func() { prepareTurnQueryBatching = false })
}

// recentQueryFixture answers single and array embedding requests; each text's
// vector starts with its query index + 1. batchStatus fails array requests.
func recentQueryFixture(t *testing.T, indices map[string]int, batchStatus int) (*sync.Mutex, *[]any) {
	t.Helper()
	var mu sync.Mutex
	requests := []any{}
	old := proxyHTTPClient
	proxyHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		mu.Lock()
		requests = append(requests, body["input"])
		mu.Unlock()
		reply := func(code int, text string) (*http.Response, error) {
			return &http.Response{StatusCode: code, Status: fmt.Sprint(code), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(text))}, nil
		}
		vectorOf := func(text string) string { return fmt.Sprintf("[%d,0.2,0.3]", indices[text]+1) }
		switch input := body["input"].(type) {
		case string:
			return reply(200, `{"data":[{"index":0,"embedding":`+vectorOf(input)+`}]}`)
		case []any:
			if batchStatus != 0 {
				return reply(batchStatus, `{}`)
			}
			rows := []string{}
			for i := len(input) - 1; i >= 0; i-- {
				rows = append(rows, fmt.Sprintf(`{"index":%d,"embedding":%s}`, i, vectorOf(fmt.Sprint(input[i]))))
			}
			return reply(200, `{"data":[`+strings.Join(rows, ",")+`]}`)
		}
		return reply(400, `{}`)
	})}
	t.Cleanup(func() { proxyHTTPClient = old })
	return &mu, &requests
}

func TestRecentTurnQueriesAreEmbeddedInOneBatch(t *testing.T) {
	for _, mode := range []string{"batch", "batch_fails"} {
		t.Run(mode, func(t *testing.T) {
			withQueryBatching(t)
			withEmbeddingLimits(t, 8, 16)
			cfg := config.Default()
			cfg.ChromaEndpoint = "http://controlled-vector.invalid"
			srv := NewServer(cfg)
			srv.Vector = &restorationRepeatedHitVector{}
			srv.VectorOpenError = nil
			raw := "Recall the silver shield."
			endpoint := "https://recent-" + mode + ".invalid/v1"
			req := dto.PrepareTurnRequest{ChatSessionID: "batched-recall", RawUserInput: &raw,
				ClientMeta: map[string]any{"embedding": map[string]any{"provider": "custom", "endpoint": endpoint, "api_key": "synthetic", "model": "fixture", "timeout_ms": 30000}}}
			for i := 0; i < 5; i++ {
				req.Messages = append(req.Messages, map[string]any{"role": "user", "content": fmt.Sprintf("Visit %d", i)}, map[string]any{"role": "assistant", "content": fmt.Sprintf("Room %d was inspected.", i)})
			}
			queries := prepareTurnRetrievalQueries(req, prepareTurnRecentConversationReferenceLimit(req.Settings))
			indices := map[string]int{}
			for i, q := range queries {
				indices[q.Text] = i
			}
			status := 0
			if mode == "batch_fails" {
				status = http.StatusServiceUnavailable
			}
			mu, requests := recentQueryFixture(t, indices, status)
			trace := srv.prepareTurnVectorShadow(context.Background(), req, 5)
			mu.Lock()
			arrays, singles := 0, 0
			for _, input := range *requests {
				if _, ok := input.([]any); ok {
					arrays++
				} else {
					singles++
				}
			}
			mu.Unlock()
			// The current input goes first on its own; the history follows as
			// one batch, or each query alone when the batch fails.
			wantSingles := 1
			if mode == "batch_fails" {
				wantSingles = len(queries)
			}
			if arrays != 1 || singles != wantSingles {
				t.Fatalf("arrays=%d singles=%d", arrays, singles)
			}
			if intFromAny(trace["query_embedding_count"], 0) != len(queries) {
				t.Fatalf("embedded %v of %d queries", trace["query_embedding_count"], len(queries))
			}
			hits := prepareTurnVectorMemorySearchResultMaps(trace)
			if len(hits) != 1 {
				t.Fatalf("hits = %v", hits)
			}
			got := []string{}
			for _, observation := range sliceFromAny(hits[0]["recall_queries"]) {
				got = append(got, extractionStringFromAny(mapFromAny(observation)["query"]))
			}
			want := []string{}
			for _, q := range queries {
				want = append(want, q.Text)
			}
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Fatalf("query provenance shifted: got=%q want=%q", got, want)
			}
		})
	}
}

func TestRecentTurnBatchSkipsKeptQueriesAndKeepsNewOnes(t *testing.T) {
	withQueryBatching(t)
	withEmbeddingLimits(t, 8, 16)
	indices := map[string]int{"a": 0, "b": 1, "c": 2}
	_, requests := recentQueryFixture(t, indices, 0)
	kept := map[string]string{}
	cfg := embeddingFixtureConfig("https://kept.invalid/v1")
	out := prepareTurnBatchQueryEmbeddings(context.Background(), cfg, []string{"a", "b", "c"},
		func(text string) bool { return text == "b" },
		func(text, embedding, _ string) { kept[text] = embedding })
	if len(out) != 2 || out[0].embedding != "[1,0.2,0.3]" || out[2].embedding != "[3,0.2,0.3]" || len(kept) != 2 {
		t.Fatalf("out=%+v kept=%v", out, kept)
	}
	if input, ok := (*requests)[0].([]any); !ok || len(input) != 2 {
		t.Fatalf("request = %v", (*requests)[0])
	}
	// One query left to ask is not worth a batch; Voyage context models never batch.
	if out := prepareTurnBatchQueryEmbeddings(context.Background(), cfg, []string{"a", "b"}, func(text string) bool { return text == "b" }, nil); out != nil {
		t.Fatalf("batched a single query: %+v", out)
	}
	voyage := completeTurnEmbeddingConfig{Provider: "voyageai", Model: "voyage-context-3", APIKey: "k", TimeoutMs: 1000}
	if out := prepareTurnBatchQueryEmbeddings(context.Background(), voyage, []string{"a", "c"}, nil, nil); out != nil {
		t.Fatalf("batched a context model: %+v", out)
	}
}

func TestRecentTurnBatchRemembersAnEndpointThatAnswersArraysWrong(t *testing.T) {
	withQueryBatching(t)
	withEmbeddingLimits(t, 8, 16)
	old := proxyHTTPClient
	proxyHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"index":0,"embedding":[1]}]}`))}, nil
	})}
	t.Cleanup(func() { proxyHTTPClient = old })
	cfg := embeddingFixtureConfig("https://one-row.invalid/v1")
	if out := prepareTurnBatchQueryEmbeddings(context.Background(), cfg, []string{"a", "b"}, nil, nil); out != nil {
		t.Fatalf("out = %+v", out)
	}
	if _, refused := embeddingBatchRefused.Load(embeddingBatchKey(cfg, normalizeEmbeddingEndpoint(cfg.Endpoint))); !refused {
		t.Fatal("a misshapen answer was not remembered")
	}
}
