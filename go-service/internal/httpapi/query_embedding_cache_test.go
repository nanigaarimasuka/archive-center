package httpapi

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeQueryEmbedder struct {
	calls  atomic.Int32
	mu     sync.Mutex
	result func(text string, call int32) (string, error)
	gate   chan struct{}
}

func (f *fakeQueryEmbedder) call(ctx context.Context, cfg completeTurnEmbeddingConfig, text string) (string, string, error) {
	n := f.calls.Add(1)
	if f.gate != nil {
		<-f.gate
	}
	embedding, err := f.result(text, n)
	return embedding, cfg.Model + "-resolved", err
}

// testClock is a settable otter clock; it never ticks.
type testClock struct{ nanos atomic.Int64 }

func (c *testClock) NowNano() int64                      { return c.nanos.Load() }
func (c *testClock) Tick(time.Duration) <-chan time.Time { return nil }
func (c *testClock) advance(d time.Duration)             { c.nanos.Add(int64(d)) }

func testQueryEmbeddingCache(f *fakeQueryEmbedder) (*queryEmbeddingCache, *testClock) {
	clock := &testClock{}
	clock.nanos.Store(time.Unix(1000, 0).UnixNano())
	c := newQueryEmbeddingCache(clock)
	c.call = f.call
	return c, clock
}

func TestQueryEmbeddingCacheReusesSuccessfulVectors(t *testing.T) {
	f := &fakeQueryEmbedder{result: func(text string, call int32) (string, error) {
		return fmt.Sprintf("[%d,0.5]", len(text)), nil
	}}
	c, now := testQueryEmbeddingCache(f)
	cfg := completeTurnEmbeddingConfig{Provider: "openai", Endpoint: "e", Model: "m", APIKey: "k"}
	ctx := context.Background()
	first, model, hit, err := c.get(ctx, cfg, "hello")
	if err != nil || hit || first != "[5,0.5]" || model != "m-resolved" {
		t.Fatalf("first: %q %q %v %v", first, model, hit, err)
	}
	again, model2, hit, err := c.get(ctx, cfg, "hello")
	if err != nil || !hit || again != first || model2 != model || f.calls.Load() != 1 {
		t.Fatalf("second: %q %q hit=%v err=%v calls=%d", again, model2, hit, err, f.calls.Load())
	}
	// Any part of the configuration or text is a different key.
	for _, other := range []completeTurnEmbeddingConfig{
		{Provider: "voyageai", Endpoint: "e", Model: "m", APIKey: "k"},
		{Provider: "openai", Endpoint: "e2", Model: "m", APIKey: "k"},
		{Provider: "openai", Endpoint: "e", Model: "m2", APIKey: "k"},
		{Provider: "openai", Endpoint: "e", Model: "m", APIKey: "k2"},
	} {
		if _, _, hit, _ := c.get(ctx, other, "hello"); hit {
			t.Fatalf("config %+v reused another key's vector", other)
		}
	}
	if _, _, hit, _ := c.get(ctx, cfg, "hello "); hit {
		t.Fatal("different text reused a vector")
	}
	// Entries expire.
	now.advance(queryEmbeddingCacheTTL)
	if _, _, hit, _ := c.get(ctx, cfg, "hello"); hit {
		t.Fatal("expired vector reused")
	}
}

func TestQueryEmbeddingCacheKeepsOnlyUsableVectors(t *testing.T) {
	results := map[string]string{"empty": "[]", "fail": "", "ok": "[1]"}
	f := &fakeQueryEmbedder{result: func(text string, call int32) (string, error) {
		if text == "fail" && call < 3 {
			return "", errors.New("provider down")
		}
		return results[text], nil
	}}
	c, _ := testQueryEmbeddingCache(f)
	cfg := completeTurnEmbeddingConfig{Model: "m"}
	ctx := context.Background()
	if _, _, _, err := c.get(ctx, cfg, "fail"); err == nil {
		t.Fatal("failure not passed on")
	}
	if _, _, hit, err := c.get(ctx, cfg, "fail"); err == nil || hit {
		t.Fatalf("failure was cached: hit=%v err=%v", hit, err)
	}
	for i := 0; i < 2; i++ {
		embedding, _, hit, err := c.get(ctx, cfg, "empty")
		if err != nil || hit || embedding != "[]" {
			t.Fatalf("empty vector: %q hit=%v err=%v", embedding, hit, err)
		}
	}
	if calls := f.calls.Load(); calls != 4 {
		t.Fatalf("calls = %d, want 4 (nothing unusable cached)", calls)
	}
}

func TestQueryEmbeddingCacheIsBounded(t *testing.T) {
	f := &fakeQueryEmbedder{result: func(text string, call int32) (string, error) { return "[1]", nil }}
	c, _ := testQueryEmbeddingCache(f)
	cfg := completeTurnEmbeddingConfig{Model: "m"}
	ctx := context.Background()
	for i := 0; i < queryEmbeddingCacheEntries+50; i++ {
		c.get(ctx, cfg, fmt.Sprint("t", i))
	}
	c.entries.CleanUp()
	if size := c.entries.EstimatedSize(); size > queryEmbeddingCacheEntries {
		t.Fatalf("entries %d, want at most %d", size, queryEmbeddingCacheEntries)
	}
}

func TestQueryEmbeddingCacheSharesOneCallPerKey(t *testing.T) {
	f := &fakeQueryEmbedder{result: func(text string, call int32) (string, error) { return "[2]", nil }, gate: make(chan struct{})}
	c, _ := testQueryEmbeddingCache(f)
	cfg := completeTurnEmbeddingConfig{Model: "m"}
	var wg sync.WaitGroup
	results := make([]string, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _, _, _ = c.get(context.Background(), cfg, "same")
		}(i)
	}
	for f.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // let the others find the call in flight
	close(f.gate)
	wg.Wait()
	if f.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", f.calls.Load())
	}
	for _, r := range results {
		if r != "[2]" {
			t.Fatalf("results %q", results)
		}
	}
}

func TestQueryEmbeddingSkipsCacheUnlessAllowed(t *testing.T) {
	if queryEmbeddingCacheAllowed(context.Background()) {
		t.Fatal("cache allowed without the request opting in")
	}
	if !queryEmbeddingCacheAllowed(withQueryEmbeddingCache(context.Background())) {
		t.Fatal("cache not allowed after opting in")
	}
}

func TestQueryEmbeddingCacheWaiterAsksAgainAfterFailedCall(t *testing.T) {
	f := &fakeQueryEmbedder{result: func(text string, call int32) (string, error) {
		if call == 1 {
			return "", errors.New("provider down")
		}
		return "[3]", nil
	}, gate: make(chan struct{})}
	c, _ := testQueryEmbeddingCache(f)
	cfg := completeTurnEmbeddingConfig{Model: "m"}
	type result struct {
		embedding string
		err       error
	}
	results := make(chan result, 2)
	ask := func() {
		embedding, _, _, err := c.get(context.Background(), cfg, "same")
		results <- result{embedding, err}
	}
	go ask()
	for f.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	go ask()
	time.Sleep(20 * time.Millisecond) // let the second find the call in flight
	close(f.gate)
	got := []result{<-results, <-results}
	failed, succeeded := 0, 0
	for _, r := range got {
		if r.err != nil {
			failed++
		} else if r.embedding == "[3]" {
			succeeded++
		}
	}
	if failed != 1 || succeeded != 1 || f.calls.Load() != 2 {
		t.Fatalf("results %+v, calls %d; want one failure and one own successful call", got, f.calls.Load())
	}
}
