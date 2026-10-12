package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/risulongmemory/archive-center-go/internal/config"
)

func embeddingRetryTestServer(t *testing.T, answer func(call int32, w http.ResponseWriter)) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer(atomic.AddInt32(&calls, 1), w)
	}))
	t.Cleanup(server.Close)
	previous := embeddingRetryDelay
	embeddingRetryDelay = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { embeddingRetryDelay = previous })
	return server, &calls
}

func embeddingRetryTestConfig(endpoint string, timeoutMs int64, retries int) completeTurnEmbeddingConfig {
	return completeTurnEmbeddingConfig{Provider: "custom", Endpoint: endpoint, Model: "m", APIKey: "k", TimeoutMs: timeoutMs, RetryCount: retries}
}

const embeddingRetryTestOK = `{"model":"m","data":[{"embedding":[0.1,0.2]}]}`

func TestEmbeddingRetriesServerErrorsUpToTheCount(t *testing.T) {
	server, calls := embeddingRetryTestServer(t, func(call int32, w http.ResponseWriter) {
		if call <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(embeddingRetryTestOK))
	})
	if _, _, err := callQueryEmbedding(context.Background(), embeddingRetryTestConfig(server.URL, 5000, 1), "q"); err == nil || *calls != 2 {
		t.Fatalf("one retry: err=%v calls=%d, want the second 503 after 2 calls", err, *calls)
	}
	atomic.StoreInt32(calls, 0)
	embedding, _, err := callQueryEmbedding(context.Background(), embeddingRetryTestConfig(server.URL, 5000, 3), "q")
	if err != nil || embedding == "" || *calls != 3 {
		t.Fatalf("three retries: embedding=%q err=%v calls=%d, want success on call 3", embedding, err, *calls)
	}
}

func TestEmbeddingRetryGivesEachTryItsOwnTimeout(t *testing.T) {
	server, calls := embeddingRetryTestServer(t, func(call int32, w http.ResponseWriter) {
		if call == 1 {
			time.Sleep(300 * time.Millisecond)
		}
		_, _ = w.Write([]byte(embeddingRetryTestOK))
	})
	if _, _, err := callQueryEmbedding(context.Background(), embeddingRetryTestConfig(server.URL, 100, 1), "q"); err != nil || *calls != 2 {
		t.Fatalf("err=%v calls=%d, want the timed-out try retried", err, *calls)
	}
}

func TestEmbeddingDoesNotRetryRefusalsOrWithoutCount(t *testing.T) {
	server, calls := embeddingRetryTestServer(t, func(call int32, w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	if _, _, err := callQueryEmbedding(context.Background(), embeddingRetryTestConfig(server.URL, 5000, 5), "q"); err == nil || *calls != 1 {
		t.Fatalf("401: err=%v calls=%d, want no retry", err, *calls)
	}
	failing, failingCalls := embeddingRetryTestServer(t, func(call int32, w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadGateway)
	})
	if _, _, err := callQueryEmbedding(context.Background(), embeddingRetryTestConfig(failing.URL, 5000, 0), "q"); err == nil || err.Error() != "embedding upstream returned 502 Bad Gateway" || *failingCalls != 1 {
		t.Fatalf("no retry count: err=%v calls=%d", err, *failingCalls)
	}
}

func TestEmbeddingRetryCountFollowsRuntimeConfig(t *testing.T) {
	srv := NewServer(config.Default())
	srv.RuntimeConfig.Synced = true
	srv.RuntimeConfig.EmbeddingRetryCount = 4
	cfg := srv.selectCompleteTurnEmbeddingConfig(map[string]any{"embedding": map[string]any{"provider": "custom", "endpoint": "http://e", "model": "m", "api_key": "k", "retry_count": 1}},
		completeTurnEmbeddingConfig{Provider: "custom", Endpoint: "http://e", Model: "m", APIKey: "k", RetryCount: 1}, srv.RuntimeConfig)
	if cfg.RetryCount != 4 {
		t.Fatalf("retry count = %d, want the synced runtime value 4", cfg.RetryCount)
	}
}
