package vector

import (
	"context"
	"math"
	"net/http"
	"strings"
	"sync"

	"github.com/maypok86/otter/v2"
)

type searchGenerationKey struct{}

// withSearchGeneration marks a search running inside the mutation fence's
// shared section; no mutation happens until the generation changes.
func withSearchGeneration(ctx context.Context, generation uint64) context.Context {
	return context.WithValue(ctx, searchGenerationKey{}, generation)
}

type sharedEmbeddingsKey struct{}

// SharedEmbeddings lets searches that run under one context fetch each stored
// embedding once. Several queries of one request return mostly the same
// documents, and their stored embeddings are almost all of every response.
type SharedEmbeddings struct {
	mu         sync.Mutex
	generation uint64
	entries    map[string]*sharedEmbedding
}

type sharedEmbedding struct {
	done      chan struct{}
	embedding []float32
	ok        bool
}

// WithSharedEmbeddings lets Search calls under ctx share stored embeddings.
// Sharing is used only for searches through the mutation fence.
func WithSharedEmbeddings(ctx context.Context) context.Context {
	return context.WithValue(ctx, sharedEmbeddingsKey{}, &SharedEmbeddings{})
}

func sharedEmbeddingsFrom(ctx context.Context) (*SharedEmbeddings, uint64, bool) {
	shared, _ := ctx.Value(sharedEmbeddingsKey{}).(*SharedEmbeddings)
	generation, fenced := ctx.Value(searchGenerationKey{}).(uint64)
	if shared == nil || !fenced {
		return nil, 0, false
	}
	return shared, generation, true
}

// claim returns the entry for each id and which of them this caller must
// fetch. Entries of an older generation are dropped: a mutation may have
// happened since, and no search of that generation is still running.
func (s *SharedEmbeddings) claim(generation uint64, ids []string) ([]*sharedEmbedding, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil || s.generation != generation {
		s.entries = map[string]*sharedEmbedding{}
		s.generation = generation
	}
	entries := make([]*sharedEmbedding, len(ids))
	var owned []string
	for i, id := range ids {
		entry, exists := s.entries[id]
		if !exists {
			entry = &sharedEmbedding{done: make(chan struct{})}
			s.entries[id] = entry
			owned = append(owned, id)
		}
		entries[i] = entry
	}
	return entries, owned
}

// sharedSearchEmbeddings returns the stored embedding of every id, fetching
// those no other search has claimed with one get. It reports false when any
// embedding could not be fetched, so the caller can query them as before.
func (s *chromaStore) sharedSearchEmbeddings(ctx context.Context, ref string, shared *SharedEmbeddings, generation uint64, ids []string) ([][]float32, bool) {
	entries, owned := shared.claim(generation, ids)
	if len(owned) > 0 {
		fetched := map[string][]float32{}
		stored := storedEmbeddingsFrom(ctx)
		missing := owned
		if stored != nil {
			missing = make([]string, 0, len(owned))
			for _, id := range owned {
				if embedding, ok := stored.get(id); ok {
					fetched[id] = embedding
				} else {
					missing = append(missing, id)
				}
			}
		}
		if len(missing) > 0 {
			var out chromaGetEmbeddingsResponse
			decode := func(data []byte) error { return decodeChromaGetEmbeddingsResponse(data, &out) }
			body := map[string]any{"ids": missing, "include": []string{"embeddings"}}
			if _, err := s.doJSONDecode(ctx, http.MethodPost, s.collectionOperationPath(ref, "get"), body, decode, http.StatusOK); err == nil {
				for i, id := range out.IDs {
					if i < len(out.Embeddings) {
						fetched[id] = out.Embeddings[i]
						if stored != nil && out.Embeddings[i] != nil {
							stored.put(id, out.Embeddings[i])
						}
					}
				}
			}
		}
		ownedEntries := map[string]*sharedEmbedding{}
		for i, id := range ids {
			ownedEntries[id] = entries[i]
		}
		shared.mu.Lock()
		for _, id := range owned {
			entry := ownedEntries[id]
			// A null embedding is treated as missing, never as a stored empty one.
			entry.embedding = fetched[id]
			entry.ok = entry.embedding != nil
			close(entry.done)
		}
		shared.mu.Unlock()
	}
	embeddings := make([][]float32, len(ids))
	for i, entry := range entries {
		select {
		case <-entry.done:
		case <-ctx.Done():
			return nil, false
		}
		if !entry.ok {
			return nil, false
		}
		embeddings[i] = entry.embedding
	}
	return embeddings, true
}

// storedEmbeddings keeps stored embeddings across requests. Every write to
// the collection goes through the mutation fence, which forgets the written
// ids (or everything, for writes it cannot attribute) before writing, so a
// kept embedding is always the one the collection holds. Searches keep
// embeddings only while they hold the fence for reading, so no search keeps
// one while a write runs.
type storedEmbeddings struct {
	byID *otter.Cache[string, []float32]
}

// About 128 MB of float32 values; past it the least useful ones are dropped.
const storedEmbeddingsMaxBytes = 128 << 20

func newStoredEmbeddings() *storedEmbeddings {
	return &storedEmbeddings{otter.Must(&otter.Options[string, []float32]{
		MaximumWeight: storedEmbeddingsMaxBytes,
		Weigher: func(id string, embedding []float32) uint32 {
			return uint32(min(4*len(embedding)+len(id), math.MaxUint32))
		},
	})}
}

type storedEmbeddingsKey struct{}

func withStoredEmbeddings(ctx context.Context, stored *storedEmbeddings) context.Context {
	return context.WithValue(ctx, storedEmbeddingsKey{}, stored)
}

func storedEmbeddingsFrom(ctx context.Context) *storedEmbeddings {
	stored, _ := ctx.Value(storedEmbeddingsKey{}).(*storedEmbeddings)
	return stored
}

func (c *storedEmbeddings) get(id string) ([]float32, bool) {
	if c == nil {
		return nil, false
	}
	return c.byID.GetIfPresent(id)
}

func (c *storedEmbeddings) put(id string, embedding []float32) {
	if c == nil {
		return
	}
	c.byID.Set(id, embedding)
}

func (c *storedEmbeddings) clear() {
	if c == nil {
		return
	}
	c.byID.InvalidateAll()
}

// forget drops the written documents' ids, or everything when an id is not
// known before the write.
func (c *storedEmbeddings) forget(docs []VectorDocument) {
	if c == nil {
		return
	}
	for _, doc := range docs {
		if strings.TrimSpace(doc.ID) == "" {
			c.clear()
			return
		}
	}
	for _, doc := range docs {
		c.byID.Invalidate(strings.TrimSpace(doc.ID))
	}
}
