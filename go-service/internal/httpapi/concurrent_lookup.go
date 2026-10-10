package httpapi

import "sync"

// lookupConcurrently calls lookup once for each distinct key, up to limit at a
// time, and returns the results by key. It is for independent store reads
// whose results are then used in their original order.
func lookupConcurrently[K comparable, V any](keys []K, limit int, lookup func(K) V) map[K]V {
	distinct := make([]K, 0, len(keys))
	seen := make(map[K]bool, len(keys))
	for _, key := range keys {
		if !seen[key] {
			seen[key] = true
			distinct = append(distinct, key)
		}
	}
	values := make([]V, len(distinct))
	slots := make(chan struct{}, max(limit, 1))
	var wg sync.WaitGroup
	for i, key := range distinct {
		wg.Add(1)
		slots <- struct{}{}
		go func(i int, key K) {
			defer wg.Done()
			defer func() { <-slots }()
			values[i] = lookup(key)
		}(i, key)
	}
	wg.Wait()
	out := make(map[K]V, len(distinct))
	for i, key := range distinct {
		out[key] = values[i]
	}
	return out
}

// Concurrent identity reads per request; the store serves them in parallel.
const identityLookupConcurrency = 8
