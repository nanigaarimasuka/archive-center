package httpapi

import (
	"context"
	"time"
)

// addHierarchyFailed records a range whose LLM summary failed. Nothing is
// saved for it, so the next run finds the range missing and tries again.
func addHierarchyFailed(layer map[string]any, fromTurn, toTurn int, err error, trace map[string]any) {
	layer["failed"] = intFromAny(layer["failed"], 0) + 1
	addHierarchyBlocked(layer, fromTurn, toTurn, "llm_failed")
	if blocked, ok := layer["blocked"].([]map[string]any); ok && len(blocked) > 0 {
		blocked[len(blocked)-1]["llm_error"] = err.Error()
		blocked[len(blocked)-1]["generation_source"] = trace["generation_source"]
	}
}

// adminRescanInOrder keeps a rescan in turn order. A turn the Critic fails for
// a retryable reason is tried again on the spot, and a turn that still fails
// stops the rescan, so no later turn is derived before it; the reprocessing
// queue keeps retrying that turn in the background. Failures a retry cannot
// fix (broken raw pairs, say) do not stop it. Session normalize uses it; other rescans leave
// failures to the queue.
type adminRescanInOrder struct {
	StoppedAt int
	Retried   []map[string]any
}

// replayRetryDelays are the waits before each retry of a failed turn.
var replayRetryDelays = []time.Duration{5 * time.Second, 15 * time.Second}

// stop reports whether the rescan must stop before its next turn because a
// turn already failed for a retryable reason, and remembers that turn.
func (o *adminRescanInOrder) stop(failedTurns []map[string]any) bool {
	if o == nil {
		return false
	}
	if o.StoppedAt == 0 {
		o.StoppedAt = firstRetryableFailedTurn(failedTurns)
	}
	return o.StoppedAt > 0
}

func firstRetryableFailedTurn(failedTurns []map[string]any) int {
	for _, item := range failedTurns {
		if stringFromAny(item["state"]) == "retryable" {
			return intFromAny(item["turn_index"], 0)
		}
	}
	return 0
}

// retry runs process, and again after each delay while it fails for a
// retryable reason. Without o it runs process once.
func (o *adminRescanInOrder) retry(ctx context.Context, turn int, process func() acceptedSourceDerivationResult) acceptedSourceDerivationResult {
	derivation := process()
	if o == nil {
		return derivation
	}
	retries := 0
	for _, delay := range replayRetryDelays {
		if derivation.State != "retryable" {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return derivation
		case <-timer.C:
		}
		derivation = process()
		retries++
	}
	if retries > 0 {
		o.Retried = append(o.Retried, map[string]any{"turn_index": turn, "retries": retries, "state": derivation.State})
	}
	return derivation
}

// settle records the stopping turn when the last turn was the one that failed.
func (o *adminRescanInOrder) settle(rescanResult map[string]any) {
	if o == nil || o.StoppedAt != 0 {
		return
	}
	o.StoppedAt = firstRetryableFailedTurn(adminSessionNormalizeMapItems(rescanResult["failed_turns"]))
}
