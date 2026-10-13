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
				progress(map[string]any{"processed": len(keys) - waiting, "candidate_count": len(keys), "progress_percent": adminJobProgressPercent(len(keys)-waiting, len(keys))})
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

// waitForQueuedReprocessing waits until the session has no memory
// reprocessing work left, then returns rescanResult with each deferred turn
// settled by its job: done turns leave the deferred list, rejected or failed
// ones become failed turns, and ones still open stay deferred. It stops early
// when the open count has not changed for longer than two worker leases.
func (s *Server) waitForQueuedReprocessing(ctx context.Context, sid string, rescanResult map[string]any, progress adminJobProgressFunc) (map[string]any, error) {
	queues, ok := s.Store.(store.SessionQueueStore)
	if !ok {
		return rescanResult, nil
	}
	stallLimit := 2*memoryWorkerLeaseDuration(s.runtimeConfigSnapshot()) + time.Minute
	if stallLimit < 15*time.Minute {
		stallLimit = 15 * time.Minute
	}
	deferredTurns := adminSessionNormalizeMapItems(rescanResult["deferred_turns"])
	total := len(deferredTurns)
	deferredTurnIndices := []int{}
	for _, item := range deferredTurns {
		deferredTurnIndices = append(deferredTurnIndices, intFromAny(item["turn_index"], 0))
	}
	open, lastOpen, lastRunning := 0, -1, -1
	lastProgress := time.Now()
	for {
		counts, err := queues.CountSessionQueues(ctx, sid)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return rescanResult, nil
		}
		open = counts.Reprocessing["pending"] + counts.Reprocessing["leased"] + counts.Reprocessing["retryable"]
		if open != lastOpen || counts.Reprocessing["leased"] != lastRunning {
			if open != lastOpen {
				lastProgress = time.Now()
			}
			lastOpen, lastRunning = open, counts.Reprocessing["leased"]
			if progress != nil {
				update := map[string]any{
					"processed":        maxInt(total-open, 0),
					"candidate_count":  total,
					"progress_percent": adminJobProgressPercent(maxInt(total-open, 0), total),
				}
				if states, err := queues.MemoryReprocessingStatesForTurns(ctx, sid, deferredTurnIndices); err == nil {
					running, waiting := []int{}, []int{}
					for _, turn := range deferredTurnIndices {
						switch states[turn] {
						case "leased":
							running = append(running, turn)
						case "pending", "retryable":
							waiting = append(waiting, turn)
						}
					}
					update["running_turns"], update["waiting_turns"] = running, waiting
				}
				progress(update)
			}
		}
		if open == 0 || time.Since(lastProgress) > stallLimit {
			break
		}
		s.wakeMemoryWorkers()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(vectorDeliveryPollInterval):
		}
	}
	states, err := queues.MemoryReprocessingStatesForTurns(ctx, sid, deferredTurnIndices)
	if err != nil {
		return rescanResult, nil
	}
	out := cloneMapAny(rescanResult)
	stillDeferred := []map[string]any{}
	failedTurns := adminSessionNormalizeMapItems(rescanResult["failed_turns"])
	succeeded := intFromAny(rescanResult["succeeded"], 0)
	for _, item := range deferredTurns {
		turn := intFromAny(item["turn_index"], 0)
		switch state := states[turn]; state {
		case "completed":
			succeeded++
		case "pending", "leased", "retryable", "":
			stillDeferred = append(stillDeferred, item)
		default:
			failedTurns = append(failedTurns, map[string]any{"turn_index": turn, "reason": "reprocessing_" + state, "state": state})
		}
	}
	out["succeeded"] = succeeded
	out["deferred"] = len(stillDeferred)
	out["queued"] = len(stillDeferred)
	out["deferred_turns"] = stillDeferred
	out["failed"] = len(failedTurns)
	out["failed_turns"] = failedTurns
	out["reprocessing_waited"] = map[string]any{"turns": total, "still_open": open}
	switch {
	case len(failedTurns) > 0:
		out["status"] = "partial_error"
	case len(stillDeferred) > 0:
		out["status"] = "deferred"
	default:
		out["status"] = "ok"
	}
	return out, nil
}
