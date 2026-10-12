package vector

import (
	"context"
	"net/http"
)

// searchQuery runs a Search query. Searches sharing embeddings query without
// them and fetch each stored embedding once; Chroma returns the same values
// either way. Anything missing falls back to the full query.
func (s *chromaStore) searchQuery(ctx context.Context, ref string, body map[string]any) (chromaQueryResponse, error) {
	var out chromaQueryResponse
	decode := func(data []byte) error { return decodeChromaQueryResponse(data, &out) }
	path := s.collectionOperationPath(ref, "query")
	if shared, generation, ok := sharedEmbeddingsFrom(ctx); ok {
		light := make(map[string]any, len(body))
		for key, value := range body {
			light[key] = value
		}
		light["include"] = []string{"metadatas", "documents", "distances"}
		// The light query is reported only if its result is used, so a
		// fallback still reports one query response.
		observed, observedBytes, observedStatus := false, 0, 0
		lightCtx := WithQueryResponseObserver(ctx, func(bytes, status int) {
			observed, observedBytes, observedStatus = true, bytes, status
		})
		report := func() {
			if observed {
				observeQueryResponse(ctx, path, observedBytes, observedStatus)
			}
		}
		if _, err := s.doJSONDecode(lightCtx, http.MethodPost, path, light, decode, http.StatusOK); err != nil {
			report()
			return chromaQueryResponse{}, err
		}
		if len(out.IDs) != 1 {
			report()
			return out, nil
		}
		if embeddings, ok := s.sharedSearchEmbeddings(ctx, ref, shared, generation, out.IDs[0]); ok {
			report()
			out.Embeddings = [][][]float32{embeddings}
			return out, nil
		}
		out = chromaQueryResponse{}
	}
	if _, err := s.doJSONDecode(ctx, http.MethodPost, path, body, decode, http.StatusOK); err != nil {
		return chromaQueryResponse{}, err
	}
	return out, nil
}
