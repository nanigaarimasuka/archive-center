package store

import (
	"context"
	"sync"
)

// MemoryVectorOperationStateCounter counts the given operations by status.
type MemoryVectorOperationStateCounter interface {
	CountMemoryVectorOperationStates(ctx context.Context, operationKeys []string) (map[string]int, error)
}

type memoryVectorOperationRecorderKey struct{}

// MemoryVectorOperationRecorder collects the outbox operations queued under
// one context: ones it inserted, and ones it brought back from a finished
// state. Operations already waiting before are not its own.
type MemoryVectorOperationRecorder struct {
	mu   sync.Mutex
	keys []string
}

func WithMemoryVectorOperationRecorder(ctx context.Context, recorder *MemoryVectorOperationRecorder) context.Context {
	return context.WithValue(ctx, memoryVectorOperationRecorderKey{}, recorder)
}

// MemoryVectorOperationRecorderFrom returns the recorder ctx carries, or nil.
func MemoryVectorOperationRecorderFrom(ctx context.Context) *MemoryVectorOperationRecorder {
	if ctx == nil {
		return nil
	}
	recorder, _ := ctx.Value(memoryVectorOperationRecorderKey{}).(*MemoryVectorOperationRecorder)
	return recorder
}

// OperationKeys returns the recorded operation keys.
func (r *MemoryVectorOperationRecorder) OperationKeys() []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.keys...)
}

// RecordMemoryVectorOperation adds an operation queued under ctx to its
// recorder, if it has one.
func RecordMemoryVectorOperation(ctx context.Context, operationKey string) {
	recorder := MemoryVectorOperationRecorderFrom(ctx)
	if recorder == nil {
		return
	}
	recorder.mu.Lock()
	recorder.keys = append(recorder.keys, operationKey)
	recorder.mu.Unlock()
}
