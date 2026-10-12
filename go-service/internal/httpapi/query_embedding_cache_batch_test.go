package httpapi

import (
	"context"
	"fmt"
	"testing"

	"github.com/risulongmemory/archive-center-go/internal/config"
	"github.com/risulongmemory/archive-center-go/internal/dto"
)

func TestRecentTurnBatchUsesTheQueryCache(t *testing.T) {
	withQueryBatching(t)
	withEmbeddingLimits(t, 8, 16)
	cfg := config.Default()
	cfg.ChromaEndpoint = "http://controlled-vector.invalid"
	srv := NewServer(cfg)
	srv.Vector = &restorationRepeatedHitVector{}
	srv.VectorOpenError = nil
	raw := "Recall the brass key."
	req := dto.PrepareTurnRequest{ChatSessionID: "cached-batch", RawUserInput: &raw,
		ClientMeta: map[string]any{"embedding": map[string]any{"provider": "custom", "endpoint": "https://cached-batch.invalid/v1", "api_key": "synthetic", "model": "fixture", "timeout_ms": 30000}}}
	for i := 0; i < 4; i++ {
		req.Messages = append(req.Messages, map[string]any{"role": "user", "content": fmt.Sprintf("Door %d", i)}, map[string]any{"role": "assistant", "content": fmt.Sprintf("Hall %d was quiet.", i)})
	}
	queries := prepareTurnRetrievalQueries(req, prepareTurnRecentConversationReferenceLimit(req.Settings))
	indices := map[string]int{}
	for i, q := range queries {
		indices[q.Text] = i
	}
	embeddingCfg := srv.completeTurnExtractionConfig(req.ClientMeta).Embedder
	kept := queries[1].Text
	sharedQueryEmbeddingCache.store(newQueryEmbeddingCacheKey(embeddingCfg, kept), "[2,0.2,0.3]", "fixture")
	_, requests := recentQueryFixture(t, indices, 0)
	srv.prepareTurnVectorShadow(withQueryEmbeddingCache(context.Background()), req, 5)
	batch := []any{}
	for _, input := range *requests {
		if items, ok := input.([]any); ok {
			batch = items
		}
	}
	if len(batch) != len(queries)-2 {
		t.Fatalf("batch = %v (requests %v)", batch, *requests)
	}
	for _, item := range batch {
		if item == kept {
			t.Fatal("a kept query was asked again")
		}
	}
	for _, q := range queries[2:] {
		if _, ok := sharedQueryEmbeddingCache.peek(embeddingCfg, q.Text); !ok {
			t.Fatalf("batched query not kept: %q", q.Text)
		}
	}
}
