package httpapi

import (
	"context"
	"github.com/risulongmemory/archive-center-go/internal/store"
	"log/slog"
	"time"
)

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
