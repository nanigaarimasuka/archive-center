package httpapi

import (
	"log/slog"
	"net/http"
)

const prepareTurnClientDisconnected = "client_disconnected"

// prepareTurnClientGone reports whether the client has gone away, for example
// because the Risu tab was closed mid-turn. Model calls and store reads already
// stop with the request context, but the steps between them would still run
// for a response nobody reads, so the handler returns at the next stage
// boundary. The HUD marks the turn invalidated rather than failed.
func (s *Server) prepareTurnClientGone(r *http.Request, workflowRequestID, stageKey string) bool {
	if r.Context().Err() == nil {
		return false
	}
	if s.TurnWorkflows != nil && workflowRequestID != "" {
		s.TurnWorkflows.invalidate(workflowRequestID, prepareTurnClientDisconnected)
	}
	slog.Info("prepare turn stopped: client disconnected", "request_id", workflowRequestID, "stage", stageKey)
	return true
}
