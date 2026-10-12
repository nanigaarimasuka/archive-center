package httpapi

import (
	"time"
)

// record adds calls measured together, such as work spread across goroutines,
// as if they ran inside the current span.
func (m *prepareTurnMeasurement) record(name string, calls int, elapsed time.Duration) {
	if m == nil || calls <= 0 {
		return
	}
	stage := m.stages[name]
	if stage == nil {
		stage = &prepareTurnMeasuredStage{}
		m.stages[name] = stage
	}
	stage.inclusive += elapsed
	stage.exclusive += elapsed
	stage.calls += int64(calls)
	if m.current != nil {
		m.current.children += elapsed
	}
}

// prepareTurnPrefetch runs a read on its own goroutine; wait returns its
// result and how long it took.
type prepareTurnPrefetch[T any] struct {
	done    chan struct{}
	rows    T
	err     error
	elapsed time.Duration
}

func startPrepareTurnPrefetch[T any](read func() (T, error)) *prepareTurnPrefetch[T] {
	p := &prepareTurnPrefetch[T]{done: make(chan struct{})}
	go func() {
		defer close(p.done)
		started := time.Now()
		p.rows, p.err = read()
		p.elapsed = time.Since(started)
	}()
	return p
}

func (p *prepareTurnPrefetch[T]) wait() (T, error, time.Duration) {
	<-p.done
	return p.rows, p.err, p.elapsed
}

// prepareTurnRecordPrefetchedRead records a prefetched read as
// prepareTurnMeasureRead records one made in place.
func prepareTurnRecordPrefetchedRead[T any](m *prepareTurnMeasurement, name string, prefetch *prepareTurnPrefetch[T]) (T, error) {
	rows, err, elapsed := prefetch.wait()
	m.record(name, 1, elapsed)
	prepareTurnMeasureReadRows(m, name, rows, err)
	return rows, err
}
