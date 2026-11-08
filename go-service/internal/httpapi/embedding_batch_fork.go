package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Several texts are embedded together: an OpenAI-compatible endpoint takes
// them as one request with an input array, and any provider can take them as
// concurrent single requests. Every embedding request of the backend shares
// one limit on how many are in flight, so concurrency never multiplies.

const (
	embeddingConcurrencyDefault = 8
	embeddingBatchSizeDefault   = 16
)

// embeddingRequests limits the embedding requests in flight. A new limit
// applies to requests that start after it; ones already running finish.
type embeddingRequestLimiter struct {
	mu    sync.Mutex
	limit int
	slots chan struct{}
	batch int
}

var embeddingRequests = newEmbeddingRequestLimiter(embeddingConcurrencyDefault, embeddingBatchSizeDefault)

func newEmbeddingRequestLimiter(limit, batch int) *embeddingRequestLimiter {
	l := &embeddingRequestLimiter{}
	l.configure(limit, batch)
	return l
}

func (l *embeddingRequestLimiter) configure(limit, batch int) {
	if limit <= 0 {
		limit = embeddingConcurrencyDefault
	}
	if batch <= 0 {
		batch = embeddingBatchSizeDefault
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if limit != l.limit {
		l.limit, l.slots = limit, make(chan struct{}, limit)
	}
	l.batch = batch
}

func (l *embeddingRequestLimiter) batchSize() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.batch
}

// acquire waits for a slot and returns its release.
func (l *embeddingRequestLimiter) acquire(ctx context.Context) (func(), error) {
	l.mu.Lock()
	slots := l.slots
	l.mu.Unlock()
	// A free slot is taken even when ctx has just ended: the request then
	// fails on ctx as it did before there was a limit.
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	default:
	}
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// embeddingBatchRefused remembers endpoints (with their model) that took the
// texts one by one but refused them as one request; they get concurrent
// single requests until the backend restarts or the setting changes.
var embeddingBatchRefused sync.Map

func embeddingBatchKey(cfg completeTurnEmbeddingConfig, endpoint string) string {
	return strings.ToLower(strings.TrimSpace(cfg.Provider)) + "\x1f" + strings.TrimSpace(endpoint) + "\x1f" + strings.TrimSpace(cfg.Model)
}

// errEmbeddingBatchShape marks a batch answer that does not fit the inputs,
// which means the endpoint does not take arrays the way it should.
var errEmbeddingBatchShape = errors.New("embedding_batch_response_shape")

// embeddingBatchEndpoint is where an OpenAI-compatible batch goes, or "" for a
// provider that only embeds one text per request.
func embeddingBatchEndpoint(cfg completeTurnEmbeddingConfig) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "openai", "custom":
		return normalizeEmbeddingEndpoint(cfg.Endpoint), true
	case "voyageai":
		return normalizeVoyageEmbeddingEndpoint(cfg.Endpoint), true
	}
	return "", false
}

// embedEachInput embeds inputs in order: in batches where the endpoint takes
// them, otherwise one request per text, both within the shared limit. Each
// request has timeout of its own.
func embedEachInput(ctx context.Context, cfg completeTurnEmbeddingConfig, timeout time.Duration, inputs []string) ([]string, string, error) {
	results := make([]string, len(inputs))
	models := make([]string, len(inputs))
	size := embeddingRequests.batchSize()
	endpoint, batchable := embeddingBatchEndpoint(cfg)
	key := embeddingBatchKey(cfg, endpoint)
	if _, refused := embeddingBatchRefused.Load(key); refused || len(inputs) == 1 {
		batchable = false
	}
	type span struct{ from, to int }
	spans := []span{}
	for from := 0; from < len(inputs); from += size {
		spans = append(spans, span{from, min(from+size, len(inputs))})
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	fail := func(err error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
		errMu.Unlock()
	}
	single := func(index int) {
		defer wg.Done()
		release, err := embeddingRequests.acquire(ctx)
		if err != nil {
			fail(err)
			return
		}
		defer release()
		callCtx, done := embeddingRequestContext(ctx, timeout)
		defer done()
		embedding, model, err := callSingleEmbedding(callCtx, cfg, inputs[index])
		if err != nil {
			fail(err)
			return
		}
		results[index], models[index] = embedding, model
	}
	for _, s := range spans {
		if !batchable || s.to-s.from == 1 {
			for index := s.from; index < s.to; index++ {
				wg.Add(1)
				go single(index)
			}
			continue
		}
		wg.Add(1)
		go func(s span) {
			defer wg.Done()
			release, err := embeddingRequests.acquire(ctx)
			if err != nil {
				fail(err)
				return
			}
			callCtx, done := embeddingRequestContext(ctx, timeout)
			embeddings, model, err := callOpenAICompatibleEmbeddingBatch(callCtx, cfg, endpoint, inputs[s.from:s.to])
			done()
			release()
			if err == nil {
				for i, embedding := range embeddings {
					results[s.from+i], models[s.from+i] = embedding, model
				}
				return
			}
			if !embeddingBatchRefusal(err) {
				fail(err)
				return
			}
			// One by one instead; if every text goes through that way, the
			// endpoint refused the array, not a text in it.
			var group sync.WaitGroup
			for index := s.from; index < s.to; index++ {
				group.Add(1)
				wg.Add(1)
				go func(index int) {
					defer group.Done()
					single(index)
				}(index)
			}
			group.Wait()
			errMu.Lock()
			succeeded := firstErr == nil
			errMu.Unlock()
			if succeeded {
				embeddingBatchRefused.Store(key, true)
			}
		}(s)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, "", firstErr
	}
	model := strings.TrimSpace(cfg.Model)
	for _, resolved := range models {
		if strings.TrimSpace(resolved) != "" {
			model = resolved
		}
	}
	return results, model, nil
}

func embeddingRequestContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return context.WithCancel(ctx)
}

// embeddingBatchRefusal says the endpoint turned the array down: a request
// shape error, or an answer that does not match the inputs. Rate limits,
// server errors and timeouts are not refusals; they are retried as before.
func embeddingBatchRefusal(err error) bool {
	if errors.Is(err, errEmbeddingBatchShape) {
		return true
	}
	var status *embeddingStatusError
	if errors.As(err, &status) {
		switch status.code {
		case http.StatusBadRequest, http.StatusNotFound, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
			return true
		}
	}
	return false
}

// callOpenAICompatibleEmbeddingBatch embeds inputs in one request and returns
// them in input order, by each row's index.
func callOpenAICompatibleEmbeddingBatch(ctx context.Context, cfg completeTurnEmbeddingConfig, endpoint string, inputs []string) ([]string, string, error) {
	body := map[string]any{"model": cfg.Model, "input": inputs}
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	resp, err := proxyHTTPClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", &embeddingStatusError{code: resp.StatusCode, message: "embedding upstream returned " + resp.Status}
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, "", err
	}
	rows := sliceFromAny(data["data"])
	if len(rows) != len(inputs) {
		return nil, "", fmt.Errorf("%w: got %d rows for %d inputs", errEmbeddingBatchShape, len(rows), len(inputs))
	}
	out := make([]string, len(inputs))
	indexed := 0
	for _, raw := range rows {
		if _, has := mapFromAny(raw)["index"]; has {
			indexed++
		}
	}
	if indexed != 0 && indexed != len(rows) {
		return nil, "", fmt.Errorf("%w: some rows have no index", errEmbeddingBatchShape)
	}
	for position, raw := range rows {
		row := mapFromAny(raw)
		index := position
		if indexed > 0 {
			index = intFromAny(row["index"], -1)
		}
		if index < 0 || index >= len(out) || out[index] != "" {
			return nil, "", fmt.Errorf("%w: row index %d", errEmbeddingBatchShape, index)
		}
		embedding := row["embedding"]
		if len(sliceFromAny(embedding)) == 0 {
			return nil, "", fmt.Errorf("%w: row %d has no embedding", errEmbeddingBatchShape, index)
		}
		b, err := json.Marshal(embedding)
		if err != nil {
			return nil, "", err
		}
		out[index] = string(b)
	}
	return out, extractionFirstNonEmpty(extractionStringFromAny(data["model"]), cfg.Model), nil
}
