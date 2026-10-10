package httpapi

import (
	"runtime"
	"sync"
)

// prepareTurnParallelFor runs fn for every index in [0, n) on up to
// GOMAXPROCS goroutines. fn must only write state owned by its index.
func prepareTurnParallelFor(n int, fn func(int)) {
	workers := min(runtime.GOMAXPROCS(0), n)
	if workers <= 1 {
		for i := 0; i < n; i++ {
			fn(i)
		}
		return
	}
	var next sync.Mutex
	index := 0
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				next.Lock()
				i := index
				index++
				next.Unlock()
				if i >= n {
					return
				}
				fn(i)
			}
		}()
	}
	wg.Wait()
}

// prepareTurnParallelScores scores texts on up to GOMAXPROCS goroutines.
// Scorers keep per-instance caches, so each goroutine builds its own with
// newScorer; a scorer's results must not depend on what it scored before.
func prepareTurnParallelScores(texts []string, newScorer func() func(string) float64) []float64 {
	scores := make([]float64, len(texts))
	workers := min(runtime.GOMAXPROCS(0), len(texts))
	if workers == 0 {
		return scores
	}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			score := newScorer()
			// Interleaved indexes spread long and short texts across workers.
			for i := w; i < len(texts); i += workers {
				scores[i] = score(texts[i])
			}
		}(w)
	}
	wg.Wait()
	return scores
}
