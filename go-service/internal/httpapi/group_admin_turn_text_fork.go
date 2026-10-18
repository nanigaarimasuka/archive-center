package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/risulongmemory/archive-center-go/internal/dto"
	"github.com/risulongmemory/archive-center-go/internal/store"
)

// adminSessionTurnTextRequest carries past turns whose text RisuAI changed in
// a way that changes no memory, such as a find and replace of a name.
type adminSessionTurnTextRequest struct {
	ChatSessionID string                          `json:"chat_session_id"`
	Entries       []dto.ChatLogRepairEntryRequest `json:"entries"`
}

// handleAdminSessionTurnText stores the new text of each given turn and
// leaves its memories, summaries and vectors as they are. Normalize instead
// derives an edited turn again.
func (s *Server) handleAdminSessionTurnText(w http.ResponseWriter, r *http.Request) {
	if !s.usesShadowWriteStore() {
		writeShadowGuard(w, "POST /admin/session-turn-text")
		return
	}
	var req adminSessionTurnTextRequest
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	sid := strings.TrimSpace(req.ChatSessionID)
	if sid == "" {
		writeBadRequest(w, "chat_session_id is required")
		return
	}
	editor, ok := s.Store.(store.HistoricalTurnTextStore)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "error", "detail": "turn_text_store_unavailable"})
		return
	}
	updated, unchanged := []int{}, []int{}
	skipped, failed := []map[string]any{}, []map[string]any{}
	for _, entry := range req.Entries {
		userText, assistantText := "", ""
		if entry.UserContent != nil {
			userText = sanitizeCriticStorageText(*entry.UserContent)
		}
		if entry.AssistantContent != nil {
			assistantText = sanitizeCriticStorageText(*entry.AssistantContent)
		}
		if entry.TurnIndex <= 0 || strings.TrimSpace(userText) == "" || strings.TrimSpace(assistantText) == "" {
			skipped = append(skipped, map[string]any{"turn_index": entry.TurnIndex, "reason": "edit_needs_both_messages"})
			continue
		}
		changed, err := editor.ReplaceHistoricalTurnText(r.Context(), store.HistoricalTurnEdit{
			ChatSessionID: sid, TurnIndex: entry.TurnIndex, UserContent: userText, AssistantContent: assistantText, EditedAt: time.Now().UTC(),
		})
		switch {
		case errors.Is(err, store.ErrNotFound):
			failed = append(failed, map[string]any{"turn_index": entry.TurnIndex, "reason": "turn_edit_message_missing"})
		case err != nil:
			failed = append(failed, map[string]any{"turn_index": entry.TurnIndex, "reason": "turn_edit_failed", "detail": err.Error()})
		case changed:
			updated = append(updated, entry.TurnIndex)
		default:
			unchanged = append(unchanged, entry.TurnIndex)
		}
	}
	status := "ok"
	if len(failed) > 0 {
		status = "partial_error"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": status, "chat_session_id": sid,
		"updated_turns": updated, "unchanged_turns": unchanged, "skipped": skipped, "failed": failed,
	})
}
