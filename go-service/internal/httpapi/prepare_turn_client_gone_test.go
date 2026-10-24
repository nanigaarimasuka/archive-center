package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/risulongmemory/archive-center-go/internal/config"
	"github.com/risulongmemory/archive-center-go/internal/store"
)

// A prepare-turn whose client disconnected stops, and its HUD entry is
// invalidated rather than failed; a connected client goes on.
func TestPrepareTurnStopsWhenTheClientIsGone(t *testing.T) {
	ledger := newTurnWorkflowHUDLedger()
	ledger.begin("req", "sess", 1)
	ledger.startStage("req", turnWorkflowStageContext)
	srv := &Server{TurnWorkflows: ledger}

	req := httptest.NewRequest(http.MethodPost, "/prepare-turn", nil)
	if srv.prepareTurnClientGone(req, "req", turnWorkflowStageContext) {
		t.Fatal("stopped while the client is still connected")
	}
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	if !srv.prepareTurnClientGone(req.WithContext(ctx), "req", turnWorkflowStageContext) {
		t.Fatal("went on after the client disconnected")
	}

	view, found := ledger.snapshot("req")
	if !found {
		t.Fatal("no HUD entry")
	}
	if view.Status != "invalidated" {
		t.Fatalf("status = %q", view.Status)
	}
	for _, stage := range view.Stages {
		if stage.Key == turnWorkflowStageContext && (stage.Status != "invalidated" || stage.ReasonCode != prepareTurnClientDisconnected) {
			t.Fatalf("context stage = %+v", stage)
		}
	}
}

// Closing the tab while preprocessing runs ends the route there: no response
// is assembled, and the turn is not reported as failed.
func TestPrepareTurnRouteStopsAfterTheClientDisconnects(t *testing.T) {
	t.Setenv("ARCHIVE_CENTER_DATA_DIR", t.TempDir())
	ctx, closeTab := context.WithCancel(context.Background())
	defer closeTab()
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		closeTab()
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"roles":{}}`}}}})
	}))
	defer provider.Close()
	cfg := config.Default()
	cfg.PromptDir = filepath.Join("..", "..", "..", "prompts")
	cfg.StoreMode = config.StoreModeDualShadow
	s := NewServer(cfg)
	s.Store = &priorityPrepareTurnStore{turnRecordingStore: &turnRecordingStore{
		returnChatLogs: []store.ChatLog{{ID: 1, ChatSessionID: "gone", TurnIndex: 1, Role: "assistant", Content: "Earlier scene."}},
		returnMemories: []store.Memory{{ID: 1, ChatSessionID: "gone", TurnIndex: 1, Importance: 8, SummaryJSON: `{"narrative_events":[{"event":"Mira kept the compass.","visibility":"public"}]}`}},
	}}
	settings := defaultMultiAgentSettings()
	settings.Enabled = true
	for role, c := range settings.Roles {
		c.Enabled = true
		c.UsePublisher = false
		c.Provider = "custom"
		c.Endpoint = provider.URL
		c.Model = "m"
		c.APIKey = "k"
		settings.Roles[role] = c
	}
	b, _ := json.Marshal(settings)
	rec := httptest.NewRecorder()
	s.handleMultiAgentSettings(rec, httptest.NewRequest(http.MethodPut, "/config/memory-preprocessing", bytes.NewReader(b)))
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}

	body, _ := json.Marshal(map[string]any{"chat_session_id": "gone", "turn_index": 2, "raw_user_input": "Go on.",
		"recent_conversation_messages": []map[string]any{{"role": "assistant", "content": "Earlier scene."}, {"role": "user", "content": "Go on."}},
		"settings":                     map[string]any{"injection_enabled": true, "max_injection_chars": 6000, "supervisor_enabled": false}})
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/prepare-turn", bytes.NewReader(body)).WithContext(ctx))

	if calls == 0 {
		t.Fatal("preprocessing never ran, so the tab was never closed")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("assembled a response after the client left: %d, %d bytes", rec.Code, rec.Body.Len())
	}
	if view, found := s.TurnWorkflows.latestSnapshotForSession("gone"); found && view.Status == "failed" {
		t.Fatalf("turn reported as failed: %+v", view)
	}
}
