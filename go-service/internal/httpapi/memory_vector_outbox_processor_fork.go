package httpapi

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/risulongmemory/archive-center-go/internal/store"
	"github.com/risulongmemory/archive-center-go/internal/vector"
)

// prepareBatchedMemoryVectorOperations embeds a claimed group's documents in
// one call (batched or concurrent, see embedEachInput) instead of one
// request after another. A document it cannot read is left to the per-item
// path, which reports it as before; when the call fails, each document is
// retried as a failed embedding would be.
func prepareBatchedMemoryVectorOperations(ctx context.Context, embeddingCfg completeTurnEmbeddingConfig, items []*store.MemoryVectorOutboxItem) (map[int64]vector.VectorDocument, map[int64]memoryVectorPreparationFailure) {
	prepared := map[int64]vector.VectorDocument{}
	failures := map[int64]memoryVectorPreparationFailure{}
	if !embeddingCfg.hasConfig() {
		return prepared, failures
	}
	type candidate struct {
		item     *store.MemoryVectorOutboxItem
		document vector.VectorDocument
	}
	candidates := []candidate{}
	texts := []string{}
	for _, item := range items {
		if item == nil || item.Operation != "upsert" {
			continue
		}
		var document vector.VectorDocument
		if err := json.Unmarshal([]byte(item.DocumentJSON), &document); err != nil ||
			len(document.Embedding) > 0 || strings.TrimSpace(document.DocumentText) == "" {
			continue
		}
		candidates = append(candidates, candidate{item: item, document: document})
		texts = append(texts, document.DocumentText)
	}
	if len(candidates) < 2 {
		return prepared, failures
	}
	embeddings, resolvedModel, err := callDocumentEmbeddings(ctx, embeddingCfg, texts)
	if err != nil {
		for _, candidate := range candidates {
			failures[candidate.item.ID] = memoryVectorPreparationFailure{Failure: "embedding materialization failed"}
		}
		return prepared, failures
	}
	if strings.TrimSpace(resolvedModel) == "" {
		resolvedModel = strings.TrimSpace(embeddingCfg.Model)
	}
	for i, candidate := range candidates {
		candidate.document.Embedding = parseFloat32JSONList(embeddings[i])
		if len(candidate.document.Embedding) == 0 {
			failures[candidate.item.ID] = memoryVectorPreparationFailure{Failure: "embedding materialization returned no vector"}
			continue
		}
		if resolvedModel != "" {
			if candidate.document.Metadata == nil {
				candidate.document.Metadata = map[string]any{}
			}
			candidate.document.Metadata["embedding_model"] = resolvedModel
		}
		prepared[candidate.item.ID] = candidate.document
	}
	return prepared, failures
}
