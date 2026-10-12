package httpapi

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"time"

	"github.com/maypok86/otter/v2"
)

// Query embeddings of recent turns are requested again on every following
// turn: each completed turn stays a retrieval query for the next few turns.
// Outside debug mode, successful query embeddings are reused, which avoids
// their cost, their occasional long waits and their failures. Debug mode
// always calls the provider, so its output stays that of perf/exact.

const (
	queryEmbeddingCacheEntries = 256
	queryEmbeddingCacheTTL     = 6 * time.Hour
)

type queryEmbeddingCacheKey struct {
	provider, endpoint, model, inputType string
	apiKey                               [32]byte // SHA-256, the key itself is not kept
	text                                 string
}

type queryEmbeddingCacheEntry struct {
	embedding string // the provider's vector as returned by callQueryEmbedding
	model     string
}

// Entries expire queryEmbeddingCacheTTL after they are stored; concurrent
// callers for one key share a single provider call.
type queryEmbeddingCache struct {
	entries *otter.Cache[queryEmbeddingCacheKey, queryEmbeddingCacheEntry]
	call    func(context.Context, completeTurnEmbeddingConfig, string) (string, string, error)
}

// clock is nil outside tests.
func newQueryEmbeddingCache(clock otter.Clock) *queryEmbeddingCache {
	return &queryEmbeddingCache{
		entries: otter.Must(&otter.Options[queryEmbeddingCacheKey, queryEmbeddingCacheEntry]{
			MaximumSize:      queryEmbeddingCacheEntries,
			ExpiryCalculator: otter.ExpiryCreating[queryEmbeddingCacheKey, queryEmbeddingCacheEntry](queryEmbeddingCacheTTL),
			Clock:            clock,
		}),
		call: callQueryEmbedding,
	}
}

var sharedQueryEmbeddingCache = newQueryEmbeddingCache(nil)

type queryEmbeddingCacheContextKey struct{}

// withQueryEmbeddingCache lets query embeddings under ctx use the cache.
func withQueryEmbeddingCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, queryEmbeddingCacheContextKey{}, true)
}

func queryEmbeddingCacheAllowed(ctx context.Context) bool {
	allowed, _ := ctx.Value(queryEmbeddingCacheContextKey{}).(bool)
	return allowed
}

func newQueryEmbeddingCacheKey(cfg completeTurnEmbeddingConfig, text string) queryEmbeddingCacheKey {
	return queryEmbeddingCacheKey{
		provider:  strings.ToLower(strings.TrimSpace(cfg.Provider)),
		endpoint:  strings.TrimSpace(cfg.Endpoint),
		model:     strings.TrimSpace(cfg.Model),
		inputType: "query",
		apiKey:    sha256.Sum256([]byte(cfg.APIKey)),
		text:      text,
	}
}

// queryEmbedding is callQueryEmbedding, served from the cache when ctx
// allows it; hit reports a reused embedding.
func queryEmbedding(ctx context.Context, cfg completeTurnEmbeddingConfig, text string) (embedding, model string, hit bool, err error) {
	if !queryEmbeddingCacheAllowed(ctx) {
		embedding, model, err = callQueryEmbedding(ctx, cfg, text)
		return embedding, model, false, err
	}
	return sharedQueryEmbeddingCache.get(ctx, cfg, text)
}

// errQueryEmbeddingUnusable keeps a failed or empty result out of the cache.
var errQueryEmbeddingUnusable = errors.New("query embedding not usable")

func (c *queryEmbeddingCache) get(ctx context.Context, cfg completeTurnEmbeddingConfig, text string) (string, string, bool, error) {
	key := newQueryEmbeddingCacheKey(cfg, text)
	var (
		loaded           bool
		embedding, model string
		err              error
	)
	entry, getErr := c.entries.Get(ctx, key, otter.LoaderFunc[queryEmbeddingCacheKey, queryEmbeddingCacheEntry](
		func(ctx context.Context, _ queryEmbeddingCacheKey) (queryEmbeddingCacheEntry, error) {
			loaded = true
			embedding, model, err = c.call(ctx, cfg, text)
			// Only usable vectors are kept; failures and empty vectors are
			// passed on as without the cache.
			if err != nil || len(parseFloat32JSONList(embedding)) == 0 {
				return queryEmbeddingCacheEntry{}, errQueryEmbeddingUnusable
			}
			return queryEmbeddingCacheEntry{embedding, model}, nil
		}))
	if loaded {
		return embedding, model, false, err
	}
	if getErr != nil {
		// The call this one waited for got nothing usable; ask for itself.
		embedding, model, err = c.call(ctx, cfg, text)
		return embedding, model, false, err
	}
	return entry.embedding, entry.model, true, nil
}

// peek returns a kept embedding without loading it.
func (c *queryEmbeddingCache) peek(cfg completeTurnEmbeddingConfig, text string) (string, bool) {
	entry, ok := c.entries.GetIfPresent(newQueryEmbeddingCacheKey(cfg, text))
	return entry.embedding, ok
}

// store keeps an embedding as a successful call would (tests).
func (c *queryEmbeddingCache) store(key queryEmbeddingCacheKey, embedding, model string) {
	c.entries.Set(key, queryEmbeddingCacheEntry{embedding, model})
}
