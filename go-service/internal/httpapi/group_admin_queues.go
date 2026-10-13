package httpapi

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/risulongmemory/archive-center-go/internal/store"
)

// adminQueueKinds are the jobs that queue a session's background work; a
// cancelled queue cancels them too.
var adminQueueKinds = []string{"session_normalize", "reindex", "rescan"}

// adminQueueEventsInterval is how often the queue stream checks the counts;
// adminQueueEventsHeartbeat resends unchanged counts so proxies keep it open.
var (
	adminQueueEventsInterval  = time.Second
	adminQueueEventsHeartbeat = 20 * time.Second
)

func adminQueueView(sid string, counts store.SessionQueueCounts) map[string]any {
	lane := func(statuses map[string]int) map[string]any {
		waiting := statuses["pending"] + statuses["needs_embedding"]
		running := statuses["leased"]
		retrying := statuses["retryable"]
		return map[string]any{
			"waiting":  waiting,
			"running":  running,
			"retrying": retrying,
			"total":    waiting + running + retrying,
		}
	}
	vector, reprocessing := lane(counts.Vector), lane(counts.Reprocessing)
	return map[string]any{
		"status":          "ok",
		"chat_session_id": sid,
		"vector":          vector,
		"reprocessing":    reprocessing,
		"total":           vector["total"].(int) + reprocessing["total"].(int),
	}
}

func (s *Server) sessionQueueStore(w http.ResponseWriter) (store.SessionQueueStore, bool) {
	queues, ok := s.Store.(store.SessionQueueStore)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"status": "not_enabled", "reason_code": "session_queue_store_unavailable"})
	}
	return queues, ok
}

// handleAdminQueues reports a session's open vector and reprocessing work.
func (s *Server) handleAdminQueues(w http.ResponseWriter, r *http.Request) {
	sid := strings.TrimSpace(r.URL.Query().Get("chat_session_id"))
	if sid == "" {
		writeBadRequest(w, "chat_session_id is required")
		return
	}
	queues, ok := s.sessionQueueStore(w)
	if !ok {
		return
	}
	counts, err := queues.CountSessionQueues(r.Context(), sid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, adminQueueView(sid, counts))
}

// handleAdminQueueEvents streams the counts as NDJSON: once at the start and
// whenever they change.
func (s *Server) handleAdminQueueEvents(w http.ResponseWriter, r *http.Request) {
	sid := strings.TrimSpace(r.URL.Query().Get("chat_session_id"))
	if sid == "" {
		writeBadRequest(w, "chat_session_id is required")
		return
	}
	queues, ok := s.sessionQueueStore(w)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"status": "stream_transport_unavailable", "reason_code": "http_flusher_unavailable"})
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	encoder := json.NewEncoder(w)
	ticker := time.NewTicker(adminQueueEventsInterval)
	defer ticker.Stop()
	var last map[string]any
	var lastSent time.Time
	for {
		counts, err := queues.CountSessionQueues(r.Context(), sid)
		if r.Context().Err() != nil {
			return
		}
		view := map[string]any{"status": "error", "chat_session_id": sid}
		if err == nil {
			view = adminQueueView(sid, counts)
		} else {
			view["detail"] = err.Error()
		}
		if !reflect.DeepEqual(view, last) || time.Since(lastSent) >= adminQueueEventsHeartbeat {
			if err := encoder.Encode(view); err != nil {
				return
			}
			flusher.Flush()
			last, lastSent = view, time.Now()
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

// handleAdminQueueCancel cancels the session's repair jobs and withdraws all
// of its background work no worker has started.
func (s *Server) handleAdminQueueCancel(w http.ResponseWriter, r *http.Request) {
	if !s.usesShadowWriteStore() {
		writeShadowGuard(w, "POST /admin/queues/cancel")
		return
	}
	var req struct {
		ChatSessionID string `json:"chat_session_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	sid := strings.TrimSpace(req.ChatSessionID)
	if sid == "" {
		writeBadRequest(w, "chat_session_id is required")
		return
	}
	queues, ok := s.sessionQueueStore(w)
	if !ok {
		return
	}
	cancelledJobs := []string{}
	if s.AdminJobs != nil {
		cancelledJobs = s.AdminJobs.cancelSessionJobs(sid, adminQueueKinds)
	}
	vector, reprocessing, err := queues.WithdrawSessionQueues(r.Context(), sid, "admin_queue_cancelled")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "detail": err.Error(), "cancelled_jobs": cancelledJobs})
		return
	}
	s.saveAuditLogBestEffort(r.Context(), &store.AuditLog{
		ChatSessionID: sid,
		EventType:     "admin_queue_cancelled",
		TargetType:    "session",
		Summary:       "Cancelled the session's queued background work",
		DetailsJSON: mustCompactJSON(map[string]any{
			"vector_withdrawn":       vector,
			"reprocessing_withdrawn": reprocessing,
			"cancelled_jobs":         cancelledJobs,
		}),
		Source:    s.storeWriteSource(),
		CreatedAt: time.Now().UTC(),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"status":                 "ok",
		"chat_session_id":        sid,
		"vector_withdrawn":       vector,
		"reprocessing_withdrawn": reprocessing,
		"cancelled_jobs":         cancelledJobs,
	})
}
