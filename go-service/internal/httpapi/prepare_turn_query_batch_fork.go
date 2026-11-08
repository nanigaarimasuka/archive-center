package httpapi

import (
	"context"
	"errors"
	"strings"
	"time"
)

// prepareTurnQueryBatching turns the batch off for tests that count one
// request per query.
var prepareTurnQueryBatching = true

type prepareTurnBatchedQuery struct {
	embedding, model string
}

// prepareTurnBatchQueryEmbeddings embeds the recent-turn queries that need a
// request in one batch request, before the per-query path runs. skip says a
// query needs no request (already kept), keep is told each new embedding.
// It tries once: on any failure it returns nothing and the per-query path
// embeds them as before, each query failing alone with its own retries.
func prepareTurnBatchQueryEmbeddings(ctx context.Context, cfg completeTurnEmbeddingConfig, texts []string, skip func(string) bool, keep func(text, embedding, model string)) map[int]prepareTurnBatchedQuery {
	endpoint, batchable := embeddingBatchEndpoint(cfg)
	if !prepareTurnQueryBatching || !batchable || usesVoyageContextualizedEmbedding(cfg) {
		return nil
	}
	key := embeddingBatchKey(cfg, endpoint)
	if _, refused := embeddingBatchRefused.Load(key); refused {
		return nil
	}
	indexes, inputs := []int{}, []string{}
	for i, text := range texts {
		if strings.TrimSpace(text) == "" || (skip != nil && skip(text)) {
			continue
		}
		indexes, inputs = append(indexes, i), append(inputs, text)
	}
	if len(inputs) < 2 || len(inputs) > embeddingRequests.batchSize() {
		return nil
	}
	release, err := embeddingRequests.acquire(ctx)
	if err != nil {
		return nil
	}
	defer release()
	callCtx, cancel := embeddingRequestContext(ctx, time.Duration(max(cfg.TimeoutMs, 0))*time.Millisecond)
	defer cancel()
	embeddings, model, err := callOpenAICompatibleEmbeddingBatch(callCtx, cfg, endpoint, inputs)
	if err != nil {
		// An answer that does not fit the array is the endpoint, not a text:
		// later turns go straight to the per-query path.
		if errors.Is(err, errEmbeddingBatchShape) {
			embeddingBatchRefused.Store(key, true)
		}
		return nil
	}
	out := make(map[int]prepareTurnBatchedQuery, len(indexes))
	for i, index := range indexes {
		out[index] = prepareTurnBatchedQuery{embeddings[i], model}
		if keep != nil {
			keep(inputs[i], embeddings[i], model)
		}
	}
	return out
}
