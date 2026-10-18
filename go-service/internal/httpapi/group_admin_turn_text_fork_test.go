package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/risulongmemory/archive-center-go/internal/config"
	"github.com/risulongmemory/archive-center-go/internal/store"
)

type turnTextFakeStore struct {
	store.Store
	stored map[int][2]string
	edits  []store.HistoricalTurnEdit
}

func (f *turnTextFakeStore) ReplaceHistoricalTurnText(_ context.Context, edit store.HistoricalTurnEdit) (bool, error) {
	f.edits = append(f.edits, edit)
	current, ok := f.stored[edit.TurnIndex]
	if !ok {
		return false, fmt.Errorf("turn %d: %w", edit.TurnIndex, store.ErrNotFound)
	}
	next := [2]string{edit.UserContent, edit.AssistantContent}
	f.stored[edit.TurnIndex] = next
	return current != next, nil
}

func TestAdminSessionTurnTextStoresTheTextOnly(t *testing.T) {
	fake := &turnTextFakeStore{stored: map[int][2]string{3: {"Mira waves", "Rook nods"}, 4: {"same", "same reply"}}}
	s := &Server{Store: fake, Cfg: config.Config{StoreMode: config.StoreModeMariaDBAuthority}}
	body := `{"chat_session_id":"sid","entries":[
		{"turn_index":3,"user_content":"Mina waves","assistant_content":"Rook nods"},
		{"turn_index":4,"user_content":"same","assistant_content":"same reply"},
		{"turn_index":5,"user_content":"gone","assistant_content":"gone"},
		{"turn_index":6,"user_content":"only input"}]}`
	rec := httptest.NewRecorder()
	s.handleAdminSessionTurnText(rec, httptest.NewRequest(http.MethodPost, "/admin/session-turn-text", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Status    string           `json:"status"`
		Updated   []int            `json:"updated_turns"`
		Unchanged []int            `json:"unchanged_turns"`
		Skipped   []map[string]any `json:"skipped"`
		Failed    []map[string]any `json:"failed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "partial_error" || !reflect.DeepEqual(got.Updated, []int{3}) || !reflect.DeepEqual(got.Unchanged, []int{4}) ||
		len(got.Skipped) != 1 || got.Skipped[0]["reason"] != "edit_needs_both_messages" ||
		len(got.Failed) != 1 || got.Failed[0]["reason"] != "turn_edit_message_missing" {
		t.Fatalf("result = %+v", got)
	}
	if fake.stored[3] != [2]string{"Mina waves", "Rook nods"} || len(fake.edits) != 3 {
		t.Fatalf("stored %v after %d edits", fake.stored, len(fake.edits))
	}
}

func TestAdminSessionTurnTextNeedsASession(t *testing.T) {
	s := &Server{Store: &turnTextFakeStore{}, Cfg: config.Config{StoreMode: config.StoreModeMariaDBAuthority}}
	rec := httptest.NewRecorder()
	s.handleAdminSessionTurnText(rec, httptest.NewRequest(http.MethodPost, "/admin/session-turn-text", strings.NewReader(`{"entries":[]}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
}
