package httpapi

import (
	"context"
	"github.com/risulongmemory/archive-center-go/internal/store"
	"time"
)

// vectorDeliveryPollInterval is how often a repair checks the vector work it
// queued.
var vectorDeliveryPollInterval = 2 * time.Second

// waitForQueuedVectorWork waits until the memory worker has written or given
// up on every vector operation queued under ctx. It stops early when none
// finishes for longer than a worker lease, reporting what still waits; a
// cancelled ctx returns its error, and the queued work is withdrawn.
func (s *Server) waitForQueuedVectorWork(ctx context.Context, progress adminJobProgressFunc) (map[string]any, error) {
	keys := store.MemoryVectorOperationRecorderFrom(ctx).OperationKeys()
	counter, ok := s.Store.(store.MemoryVectorOperationStateCounter)
	if len(keys) == 0 || !ok {
		return nil, nil
	}
	stallLimit := memoryWorkerLeaseDuration(s.runtimeConfigSnapshot()) + time.Minute
	if stallLimit < 10*time.Minute {
		stallLimit = 10 * time.Minute
	}
	waiting, lastWaiting := 0, -1
	lastProgress := time.Now()
	var counts map[string]int
	for {
		var err error
		if counts, err = counter.CountMemoryVectorOperationStates(ctx, keys); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return map[string]any{"queued": len(keys), "error": err.Error()}, nil
		}
		waiting = counts["pending"] + counts["retryable"] + counts["needs_embedding"] + counts["leased"]
		if waiting != lastWaiting {
			lastWaiting, lastProgress = waiting, time.Now()
			if progress != nil {
				progress(map[string]any{"processed": len(keys) - waiting, "candidate_count": len(keys)})
			}
		}
		if waiting == 0 || time.Since(lastProgress) > stallLimit {
			break
		}
		s.wakeMemoryWorkers()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(vectorDeliveryPollInterval):
		}
	}
	return map[string]any{
		"queued":    len(keys),
		"completed": counts["completed"],
		"rejected":  counts["stale_rejected"],
		"failed":    counts["permanent"] + counts["failed"],
		"waiting":   waiting,
	}, nil
}
