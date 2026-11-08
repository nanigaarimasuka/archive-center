package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/risulongmemory/archive-center-go/internal/store"
	"github.com/risulongmemory/archive-center-go/internal/vector"
)

// embeddingFixture answers OpenAI-compatible embedding requests. A text's
// vector is [n] for the n in "t<n>"; arrays are answered in reverse order with
// their indexes unless refuseArrays, and badText fails on its own.
type embeddingFixture struct {
	mu           sync.Mutex
	refuseArrays bool
	status       int
	badText      string
	requests     []any
	active, peak atomic.Int32
	hold         time.Duration
}

func (f *embeddingFixture) install(t *testing.T) {
	t.Helper()
	old := proxyHTTPClient
	proxyHTTPClient = &http.Client{Transport: roundTripFunc(f.roundTrip)}
	t.Cleanup(func() { proxyHTTPClient = old })
}

func embeddingFixtureVector(text string) string {
	return "[" + strings.TrimPrefix(text, "t") + "]"
}

func (f *embeddingFixture) roundTrip(r *http.Request) (*http.Response, error) {
	a := f.active.Add(1)
	defer f.active.Add(-1)
	for p := f.peak.Load(); a > p && !f.peak.CompareAndSwap(p, a); p = f.peak.Load() {
	}
	if f.hold > 0 {
		time.Sleep(f.hold)
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.requests = append(f.requests, body["input"])
	f.mu.Unlock()
	reply := func(code int, text string) (*http.Response, error) {
		return &http.Response{StatusCode: code, Status: fmt.Sprint(code), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(text))}, nil
	}
	if f.status != 0 {
		return reply(f.status, `{}`)
	}
	switch input := body["input"].(type) {
	case string:
		if input == f.badText {
			return reply(400, `{"error":"bad text"}`)
		}
		return reply(200, `{"data":[{"index":0,"embedding":`+embeddingFixtureVector(input)+`}],"model":"m"}`)
	case []any:
		if f.refuseArrays {
			return reply(400, `{"error":"input must be a string"}`)
		}
		rows := []string{}
		for i := len(input) - 1; i >= 0; i-- {
			text := fmt.Sprint(input[i])
			if text == f.badText {
				return reply(400, `{"error":"bad text"}`)
			}
			rows = append(rows, fmt.Sprintf(`{"index":%d,"embedding":%s}`, i, embeddingFixtureVector(text)))
		}
		return reply(200, `{"data":[`+strings.Join(rows, ",")+`],"model":"m"}`)
	}
	return reply(400, `{}`)
}

func (f *embeddingFixture) arrayRequests() (arrays, singles int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, input := range f.requests {
		if _, ok := input.([]any); ok {
			arrays++
		} else {
			singles++
		}
	}
	return arrays, singles
}

func embeddingFixtureConfig(endpoint string) completeTurnEmbeddingConfig {
	return completeTurnEmbeddingConfig{Provider: "custom", Endpoint: endpoint, APIKey: "k", Model: "m", TimeoutMs: 5000}
}

func embeddingInputs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("t%d", i+1)
	}
	return out
}

func withEmbeddingLimits(t *testing.T, concurrency, batch int) {
	t.Helper()
	embeddingRequests.configure(concurrency, batch)
	t.Cleanup(func() { embeddingRequests.configure(embeddingConcurrencyDefault, embeddingBatchSizeDefault) })
}

func checkEmbeddingOrder(t *testing.T, got []string, n int) {
	t.Helper()
	if len(got) != n {
		t.Fatalf("got %d embeddings for %d inputs", len(got), n)
	}
	for i, embedding := range got {
		if embedding != fmt.Sprintf("[%d]", i+1) {
			t.Fatalf("embedding %d = %s: %v", i, embedding, got)
		}
	}
}

func TestEmbeddingBatchesInInputOrder(t *testing.T) {
	withEmbeddingLimits(t, 8, 16)
	fixture := &embeddingFixture{}
	fixture.install(t)
	got, model, err := callDocumentEmbeddings(context.Background(), embeddingFixtureConfig("https://batch.test/v1"), embeddingInputs(20))
	if err != nil || model != "m" {
		t.Fatalf("err=%v model=%q", err, model)
	}
	checkEmbeddingOrder(t, got, 20)
	if arrays, singles := fixture.arrayRequests(); arrays != 2 || singles != 0 {
		t.Fatalf("arrays=%d singles=%d", arrays, singles)
	}
}

func TestEmbeddingFallsBackToConcurrentSinglesAndRemembers(t *testing.T) {
	withEmbeddingLimits(t, 3, 16)
	fixture := &embeddingFixture{refuseArrays: true, hold: 20 * time.Millisecond}
	fixture.install(t)
	cfg := embeddingFixtureConfig("https://refuses.test/v1")
	got, _, err := callDocumentEmbeddings(context.Background(), cfg, embeddingInputs(9))
	if err != nil {
		t.Fatal(err)
	}
	checkEmbeddingOrder(t, got, 9)
	if arrays, singles := fixture.arrayRequests(); arrays != 1 || singles != 9 {
		t.Fatalf("first call: arrays=%d singles=%d", arrays, singles)
	}
	if peak := fixture.peak.Load(); peak > 3 || peak < 2 {
		t.Fatalf("peak concurrency %d, limit 3", peak)
	}
	// The refusal is remembered: no array request the next time.
	if _, _, err := callDocumentEmbeddings(context.Background(), cfg, embeddingInputs(4)); err != nil {
		t.Fatal(err)
	}
	if arrays, singles := fixture.arrayRequests(); arrays != 1 || singles != 13 {
		t.Fatalf("second call: arrays=%d singles=%d", arrays, singles)
	}
}

func TestEmbeddingBadTextIsNotABatchRefusal(t *testing.T) {
	withEmbeddingLimits(t, 4, 16)
	fixture := &embeddingFixture{badText: "t3"}
	fixture.install(t)
	cfg := embeddingFixtureConfig("https://badtext.test/v1")
	if _, _, err := callDocumentEmbeddings(context.Background(), cfg, embeddingInputs(5)); err == nil {
		t.Fatal("a text the provider rejects did not fail the call")
	}
	if _, refused := embeddingBatchRefused.Load(embeddingBatchKey(cfg, normalizeEmbeddingEndpoint(cfg.Endpoint))); refused {
		t.Fatal("one bad text made the endpoint lose batching")
	}
}

func TestEmbeddingRateLimitIsRetriedNotRefused(t *testing.T) {
	withEmbeddingLimits(t, 4, 16)
	fixture := &embeddingFixture{status: http.StatusTooManyRequests}
	fixture.install(t)
	cfg := embeddingFixtureConfig("https://limited.test/v1")
	_, _, err := callDocumentEmbeddings(context.Background(), cfg, embeddingInputs(3))
	if err == nil || !embeddingErrorRetryable(err) {
		t.Fatalf("err = %v", err)
	}
	if arrays, singles := fixture.arrayRequests(); arrays != 1 || singles != 0 {
		t.Fatalf("a rate limit fell back to singles: arrays=%d singles=%d", arrays, singles)
	}
}

func TestEmbeddingBatchShapeMismatchIsARefusal(t *testing.T) {
	for _, body := range []string{
		`{"data":[{"index":0,"embedding":[1]}]}`,
		`{"data":[{"index":0,"embedding":[1]},{"embedding":[2]}]}`,
		`{"data":[{"index":0,"embedding":[1]},{"index":0,"embedding":[2]}]}`,
		`{"data":[{"index":0,"embedding":[1]},{"index":1,"embedding":[]}]}`,
	} {
		old := proxyHTTPClient
		proxyHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
		_, _, err := callOpenAICompatibleEmbeddingBatch(context.Background(), embeddingFixtureConfig("https://shape.test/v1"), "https://shape.test/v1/embeddings", []string{"t1", "t2"})
		proxyHTTPClient = old
		if !embeddingBatchRefusal(err) {
			t.Errorf("%s: err = %v", body, err)
		}
	}
}

func TestOutboxEmbedsAClaimedGroupTogether(t *testing.T) {
	withEmbeddingLimits(t, 8, 16)
	fixture := &embeddingFixture{}
	fixture.install(t)
	doc := func(text string, embedding []float32) string {
		b, _ := json.Marshal(vector.VectorDocument{ID: text, DocumentText: text, Embedding: embedding})
		return string(b)
	}
	items := []*store.MemoryVectorOutboxItem{
		{ID: 1, Operation: "upsert", DocumentJSON: doc("t1", nil)},
		{ID: 2, Operation: "upsert", DocumentJSON: doc("t2", []float32{9})}, // already embedded
		{ID: 3, Operation: "upsert", DocumentJSON: "{"},                     // left to the per-item path
		{ID: 4, Operation: "upsert", DocumentJSON: doc("t4", nil)},
	}
	cfg := embeddingFixtureConfig("https://outbox.test/v1")
	prepared, failures := prepareBatchedMemoryVectorOperations(context.Background(), cfg, items)
	if len(failures) != 0 || len(prepared) != 2 || prepared[1].Embedding[0] != 1 || prepared[4].Embedding[0] != 4 ||
		prepared[1].Metadata["embedding_model"] != "m" {
		t.Fatalf("prepared=%+v failures=%+v", prepared, failures)
	}
	if arrays, singles := fixture.arrayRequests(); arrays != 1 || singles != 0 {
		t.Fatalf("arrays=%d singles=%d", arrays, singles)
	}
	fixture.status = http.StatusServiceUnavailable
	cfg.RetryCount = 0
	prepared, failures = prepareBatchedMemoryVectorOperations(context.Background(), cfg, items)
	if len(prepared) != 0 || len(failures) != 2 || failures[1].Permanent || failures[4].Failure != "embedding materialization failed" {
		t.Fatalf("on failure: prepared=%+v failures=%+v", prepared, failures)
	}
}

func TestEmbeddingLimitsFollowTheRuntimeConfig(t *testing.T) {
	withEmbeddingLimits(t, embeddingConcurrencyDefault, embeddingBatchSizeDefault)
	s := &Server{}
	s.updateRuntimeConfig(map[string]any{"embeddingConcurrency": 3, "embeddingBatchSize": 500})
	if cap(embeddingRequests.slots) != 3 || embeddingRequests.batchSize() != 128 {
		t.Fatalf("limit=%d batch=%d", cap(embeddingRequests.slots), embeddingRequests.batchSize())
	}
	if s.runtimeConfigSaved["embeddingConcurrency"] == nil || s.runtimeConfigSaved["embeddingBatchSize"] == nil {
		t.Fatalf("not kept across restarts: %v", s.runtimeConfigSaved)
	}
	// A sync without the keys leaves the defaults in place.
	s2 := &Server{}
	s2.updateRuntimeConfig(map[string]any{"topK": 5})
	if cap(embeddingRequests.slots) != embeddingConcurrencyDefault || embeddingRequests.batchSize() != embeddingBatchSizeDefault {
		t.Fatalf("defaults lost: limit=%d batch=%d", cap(embeddingRequests.slots), embeddingRequests.batchSize())
	}
}
