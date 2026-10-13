package httpapi

import (
	"context"
	"sync"
)

// Chapter, arc, and saga summaries take one LLM call each, often minutes
// long. A job that asks for them puts a reporter on its context, so its
// progress shows which summary is being made while nothing else moves.
type summaryProgressKey struct{}

type summaryProgressReporter struct {
	mu     sync.Mutex
	report adminJobProgressFunc
	made   map[string]int
}

// withSummaryProgress makes the summaries started under ctx show in report.
func withSummaryProgress(ctx context.Context, report adminJobProgressFunc) context.Context {
	if report == nil {
		return ctx
	}
	return context.WithValue(ctx, summaryProgressKey{}, &summaryProgressReporter{report: report, made: map[string]int{}})
}

// reportSummaryInProgress says a summary of layer over the turn range has
// started; count is how many of that layer this job has started so far.
func reportSummaryInProgress(ctx context.Context, layer string, fromTurn, toTurn int) {
	reporter, _ := ctx.Value(summaryProgressKey{}).(*summaryProgressReporter)
	if reporter == nil {
		return
	}
	reporter.mu.Lock()
	reporter.made[layer]++
	count := reporter.made[layer]
	reporter.mu.Unlock()
	reporter.report(map[string]any{"current_summary": map[string]any{
		"layer": layer, "from_turn": fromTurn, "to_turn": toTurn, "count": count,
	}})
}

// clearSummaryProgress says no summary is being made any more.
func clearSummaryProgress(ctx context.Context) {
	if reporter, _ := ctx.Value(summaryProgressKey{}).(*summaryProgressReporter); reporter != nil {
		reporter.report(map[string]any{"current_summary": nil})
	}
}
