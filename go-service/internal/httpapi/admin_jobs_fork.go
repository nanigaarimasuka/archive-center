package httpapi

import (
	"context"
	"github.com/risulongmemory/archive-center-go/internal/store"
	"log/slog"
	"strings"
	"time"
)

// cancelSessionJobs cancels the session's running jobs of the given kinds
// and returns their ids.
func (m *adminJobManager) cancelSessionJobs(sid string, kinds []string) []string {
	ids := []string{}
	if m == nil {
		return ids
	}
	m.mu.RLock()
	for _, id := range m.order {
		job := m.jobs[id]
		if job == nil || job.SessionID != sid || adminJobTerminal(job.Status) {
			continue
		}
		for _, kind := range kinds {
			if strings.EqualFold(job.Kind, kind) {
				ids = append(ids, id)
				break
			}
		}
	}
	m.mu.RUnlock()
	for _, id := range ids {
		m.cancelJob(id)
	}
	return ids
}

// withdrawVectorWorkOnCancel makes a cancelled job take back the vector
// operations it queued that no worker has started: cancelling a session
// repair leaves no part of it to run later.
func (s *Server) withdrawVectorWorkOnCancel(work func(context.Context, adminJobProgressFunc) (map[string]any, error)) func(context.Context, adminJobProgressFunc) (map[string]any, error) {
	return func(ctx context.Context, progress adminJobProgressFunc) (map[string]any, error) {
		recorder := &store.MemoryVectorOperationRecorder{}
		result, err := work(store.WithMemoryVectorOperationRecorder(ctx, recorder), progress)
		if ctx.Err() == nil {
			return result, err
		}
		keys := recorder.OperationKeys()
		withdrawer, ok := s.Store.(store.MemoryVectorOperationWithdrawer)
		if len(keys) == 0 || !ok {
			return result, err
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		withdrawn, withdrawErr := withdrawer.WithdrawMemoryVectorOperations(cleanupCtx, keys, "admin_job_cancelled")
		if withdrawErr != nil {
			slog.Error("cancelled job left its vector work queued", "error", withdrawErr.Error(), "queued", len(keys))
		} else {
			slog.Info("cancelled job withdrew its queued vector work", "withdrawn", withdrawn, "queued", len(keys))
		}
		return result, err
	}
}
