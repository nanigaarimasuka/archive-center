package httpapi

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/risulongmemory/archive-center-go/internal/config"
	"github.com/risulongmemory/archive-center-go/internal/store"
)

type withdrawRecordingStore struct {
	*memoryFakeStore
	withdrawn [][]string
}

func (s *withdrawRecordingStore) WithdrawMemoryVectorOperations(_ context.Context, keys []string, _ string) (int, error) {
	s.withdrawn = append(s.withdrawn, keys)
	return len(keys), nil
}

func TestCancelledJobWithdrawsTheVectorWorkItQueued(t *testing.T) {
	srv := NewServer(config.Default())
	fake := &withdrawRecordingStore{memoryFakeStore: &memoryFakeStore{}}
	srv.Store = fake
	work := srv.withdrawVectorWorkOnCancel(func(ctx context.Context, _ adminJobProgressFunc) (map[string]any, error) {
		store.RecordMemoryVectorOperation(ctx, "op-1")
		store.RecordMemoryVectorOperation(ctx, "op-2")
		return nil, ctx.Err()
	})

	if _, err := work(context.Background(), nil); err != nil || len(fake.withdrawn) != 0 {
		t.Fatalf("finished job withdrew %v (err=%v)", fake.withdrawn, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	work(ctx, nil)
	if want := [][]string{{"op-1", "op-2"}}; !reflect.DeepEqual(fake.withdrawn, want) {
		t.Fatalf("withdrawn = %v, want %v", fake.withdrawn, want)
	}
}

type vectorStateCountingStore struct {
	*memoryFakeStore
	states []map[string]int
	calls  int
}

func (s *vectorStateCountingStore) CountMemoryVectorOperationStates(context.Context, []string) (map[string]int, error) {
	state := s.states[min(s.calls, len(s.states)-1)]
	s.calls++
	return state, nil
}

func TestSessionNormalizeWaitsForTheVectorWorkItQueued(t *testing.T) {
	previous := vectorDeliveryPollInterval
	vectorDeliveryPollInterval = time.Millisecond
	defer func() { vectorDeliveryPollInterval = previous }()
	srv := NewServer(config.Default())
	fake := &vectorStateCountingStore{memoryFakeStore: &memoryFakeStore{}, states: []map[string]int{
		{"needs_embedding": 2, "pending": 1},
		{"needs_embedding": 1, "completed": 2},
		{"completed": 2, "permanent": 1},
	}}
	srv.Store = fake
	ctx := store.WithMemoryVectorOperationRecorder(context.Background(), &store.MemoryVectorOperationRecorder{})
	for _, key := range []string{"op-1", "op-2", "op-3"} {
		store.RecordMemoryVectorOperation(ctx, key)
	}
	seen := []int{}
	delivery, err := srv.waitForQueuedVectorWork(ctx, func(progress map[string]any) {
		seen = append(seen, intFromAny(progress["processed"], -1))
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"queued": 3, "completed": 2, "rejected": 0, "failed": 1, "waiting": 0}
	if !reflect.DeepEqual(delivery, want) || !reflect.DeepEqual(seen, []int{0, 2, 3}) {
		t.Fatalf("delivery = %v progress = %v", delivery, seen)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	fake.calls = 0
	if _, err := srv.waitForQueuedVectorWork(cancelled, nil); err == nil {
		t.Fatal("a cancelled wait reported success")
	}
	if delivery, err := srv.waitForQueuedVectorWork(context.Background(), nil); err != nil || delivery != nil {
		t.Fatalf("nothing queued: delivery = %v, err = %v", delivery, err)
	}
}
