package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/risulongmemory/archive-center-go/internal/config"
	"github.com/risulongmemory/archive-center-go/internal/store"
)

type sessionQueueFakeStore struct {
	*memoryFakeStore
	counts    []store.SessionQueueCounts
	calls     int
	states    map[int]string
	withdrawn []string
}

func (s *sessionQueueFakeStore) CountSessionQueues(context.Context, string) (store.SessionQueueCounts, error) {
	counts := s.counts[min(s.calls, len(s.counts)-1)]
	s.calls++
	return counts, nil
}

func (s *sessionQueueFakeStore) WithdrawSessionQueues(_ context.Context, sid, _ string) (int, int, error) {
	s.withdrawn = append(s.withdrawn, sid)
	return 5, 2, nil
}

func (s *sessionQueueFakeStore) MemoryReprocessingStatesForTurns(context.Context, string, []int) (map[int]string, error) {
	return s.states, nil
}

func newQueueTestServer(fake *sessionQueueFakeStore) (*Server, *http.ServeMux) {
	cfg := config.Default()
	cfg.StoreMode = config.StoreModeMariaDBShadow
	srv := NewServer(cfg)
	srv.Store = fake
	srv.StoreOpenError = nil
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	return srv, mux
}

func TestAdminQueuesReportsOpenWorkByLane(t *testing.T) {
	_, mux := newQueueTestServer(&sessionQueueFakeStore{memoryFakeStore: &memoryFakeStore{}, counts: []store.SessionQueueCounts{{
		Vector:       map[string]int{"pending": 2, "needs_embedding": 3, "leased": 1},
		Reprocessing: map[string]int{"retryable": 1},
	}}})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/queues?chat_session_id=s1", nil))
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !reflect.DeepEqual(got["vector"], map[string]any{"waiting": 5.0, "running": 1.0, "retrying": 0.0, "total": 6.0}) ||
		!reflect.DeepEqual(got["reprocessing"], map[string]any{"waiting": 0.0, "running": 0.0, "retrying": 1.0, "total": 1.0}) ||
		got["total"] != 7.0 {
		t.Fatalf("queues = %v", got)
	}
}

func TestAdminQueueEventsStreamsOnlyChanges(t *testing.T) {
	previous := adminQueueEventsInterval
	adminQueueEventsInterval = time.Millisecond
	defer func() { adminQueueEventsInterval = previous }()
	same := store.SessionQueueCounts{Vector: map[string]int{"pending": 1}, Reprocessing: map[string]int{}}
	_, mux := newQueueTestServer(&sessionQueueFakeStore{memoryFakeStore: &memoryFakeStore{}, counts: []store.SessionQueueCounts{
		same, same, same, {Vector: map[string]int{}, Reprocessing: map[string]int{}},
	}})
	server := httptest.NewServer(mux)
	defer server.Close()
	resp, err := http.Get(server.URL + "/admin/queues/events?chat_session_id=s1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	lines := bufio.NewScanner(resp.Body)
	totals := []float64{}
	for len(totals) < 2 && lines.Scan() {
		var event map[string]any
		if err := json.Unmarshal(lines.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		totals = append(totals, event["total"].(float64))
	}
	if !reflect.DeepEqual(totals, []float64{1, 0}) {
		t.Fatalf("streamed totals = %v, want one event per change", totals)
	}
}

func TestAdminQueueCancelCancelsRepairJobsAndWithdrawsWork(t *testing.T) {
	fake := &sessionQueueFakeStore{memoryFakeStore: &memoryFakeStore{}}
	srv, mux := newQueueTestServer(fake)
	block := make(chan struct{})
	defer close(block)
	job := srv.AdminJobs.start("session_normalize", "s1", map[string]any{}, func(ctx context.Context, _ adminJobProgressFunc) (map[string]any, error) {
		select {
		case <-ctx.Done():
		case <-block:
		}
		return nil, ctx.Err()
	})
	other := srv.AdminJobs.start("session_normalize", "s2", map[string]any{}, func(ctx context.Context, _ adminJobProgressFunc) (map[string]any, error) {
		select {
		case <-ctx.Done():
		case <-block:
		}
		return nil, ctx.Err()
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/queues/cancel", strings.NewReader(`{"chat_session_id":"s1"}`)))
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !reflect.DeepEqual(got["cancelled_jobs"], []any{job["job_id"]}) || got["vector_withdrawn"] != 5.0 || got["reprocessing_withdrawn"] != 2.0 ||
		!reflect.DeepEqual(fake.withdrawn, []string{"s1"}) {
		t.Fatalf("cancel = %v withdrawn = %v", got, fake.withdrawn)
	}
	if snapshot, _ := srv.AdminJobs.get(job["job_id"].(string)); snapshot["status"] != "cancelled" {
		t.Fatalf("s1 job status = %v", snapshot["status"])
	}
	if snapshot, _ := srv.AdminJobs.get(other["job_id"].(string)); snapshot["status"] == "cancelled" {
		t.Fatal("another session's job was cancelled")
	}
}

func TestSessionNormalizeWaitsForQueuedReprocessing(t *testing.T) {
	previous := vectorDeliveryPollInterval
	vectorDeliveryPollInterval = time.Millisecond
	defer func() { vectorDeliveryPollInterval = previous }()
	srv, _ := newQueueTestServer(&sessionQueueFakeStore{memoryFakeStore: &memoryFakeStore{},
		counts: []store.SessionQueueCounts{
			{Reprocessing: map[string]int{"pending": 2, "leased": 1}},
			{Reprocessing: map[string]int{"leased": 1}},
			{Reprocessing: map[string]int{}},
		},
		states: map[int]string{3: "completed", 4: "permanent", 5: "completed"},
	})
	rescan := map[string]any{
		"status": "deferred", "succeeded": 1, "deferred": 3, "queued": 3, "failed": 0,
		"deferred_turns": []map[string]any{{"turn_index": 3}, {"turn_index": 4}, {"turn_index": 5}},
	}
	got, err := srv.waitForQueuedReprocessing(context.Background(), "s1", rescan, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["status"] != "partial_error" || got["succeeded"] != 3 || got["deferred"] != 0 || got["failed"] != 1 ||
		intFromAny(adminSessionNormalizeMapItems(got["failed_turns"])[0]["turn_index"], 0) != 4 {
		t.Fatalf("settled rescan = %v", got)
	}
	if rescan["status"] != "deferred" {
		t.Fatal("the rescan result passed in was changed")
	}
}

func TestSessionNormalizeProgressAdapterKeepsPercentAndClearsOldTurns(t *testing.T) {
	updates := []map[string]any{}
	adapter := adminSessionNormalizeProgressAdapter(func(progress map[string]any) {
		updates = append(updates, progress)
	}, "critic_rescan_backfill", 18, 52)
	adapter(map[string]any{"current_turn": 7})
	adapter(map[string]any{"progress_percent": 50})
	if _, set := updates[0]["progress_percent"]; set || updates[0]["current_turn"] != 7 {
		t.Fatalf("turn update = %v, want the turn without moving the bar", updates[0])
	}
	if updates[1]["progress_percent"] != 44 || updates[1]["current_turn"] != nil {
		t.Fatalf("percent update = %v, want 44%% and no stale turn", updates[1])
	}
}
