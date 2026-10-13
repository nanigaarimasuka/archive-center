package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/risulongmemory/archive-center-go/internal/config"
	"github.com/risulongmemory/archive-center-go/internal/store"
)

type presentationTopologyStore struct {
	store.Store
	snapshot store.WorldlineTopologySnapshot
	err      error
	anchor   string
	limit    int
}

type timelineViewResponse struct {
	Groups            []presentationTimelineGroup `json:"groups"`
	WorldlineTopology worldlineTopologyViewModel  `json:"worldline_topology"`
}

func (s *presentationTopologyStore) GetWorldlineTopologySnapshot(_ context.Context, anchorSessionID string, limit int) (store.WorldlineTopologySnapshot, error) {
	s.anchor = anchorSessionID
	s.limit = limit
	return s.snapshot, s.err
}

func TestBuildTimelinePageViewGroupsItemsByTurn(t *testing.T) {
	view := buildTimelinePageView([]map[string]any{
		{"type": "chat_log", "id": 1, "role": "user", "turn_index": 2, "content": "input", "chat_session_id": "char_1_cid_a"},
		{"type": "memory", "id": 2, "turn_index": 2, "summary_json": `{"summary":"remembered event"}`, "chat_session_id": "char_1_cid_a"},
		{"type": "chat_log", "id": 3, "role": "assistant", "turn_index": -1, "content": "opening", "chat_session_id": "char_1_cid_a"},
	})
	if view.CurrentTurn != 2 || len(view.Items) != 3 || len(view.Groups) != 2 {
		t.Fatalf("view=%+v", view)
	}
	group := view.Groups[0]
	if group.Key != "turn:2" || group.ItemCount != 2 || group.Preview != "remembered event" ||
		strings.Join(group.ItemKeys, ",") != "chat_log:1,memory:2" || group.Counts["memory"] != 1 {
		t.Fatalf("group=%+v", group)
	}
	if view.Groups[1].Kind != "starter" {
		t.Fatalf("starter group=%+v", view.Groups[1])
	}
	encoded, _ := json.Marshal(group)
	if strings.Contains(string(encoded), "summary_json") {
		t.Fatalf("a group repeats its items instead of naming them: %s", encoded)
	}
}

func TestTimelineViewAttachesSelectedFamilyTopology(t *testing.T) {
	fake := &presentationTopologyStore{
		Store: store.NewNoopStore(),
		snapshot: store.WorldlineTopologySnapshot{
			StableCharacterID: "stable-character",
			AnchorSessionID:   "selected-session",
			SessionIDs:        []string{"current-session", "root-session", "selected-session"},
			LineageRecords: []store.ForkLineageRecord{
				confirmedWorldlineTopologyRecord("selected-session", "root-session", 3, "user", "msg-selected"),
				confirmedWorldlineTopologyRecord("current-session", "selected-session", 3, "char", "msg-current"),
			},
			CompletedTurns: append(
				worldlineCompletedTurns("root-session", 1, 2, 3),
				append(worldlineCompletedTurns("selected-session", 1, 2, 3), worldlineCompletedTurns("current-session", 1, 2, 3, 4)...)...,
			),
		},
	}
	srv := NewServer(config.Default())
	srv.Store = fake
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/timeline?sessionId=selected-session&currentSessionId=current-session&view=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response timelineViewResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if fake.anchor != "selected-session" || fake.limit != worldlineTopologyFamilyLimit {
		t.Fatalf("snapshot query anchor=%q limit=%d", fake.anchor, fake.limit)
	}
	topology := response.WorldlineTopology
	if topology.ContractVersion != worldlineTopologyViewModelContract || topology.State != "ready" || topology.CurrentSessionID != "current-session" || topology.SelectedSessionID != "selected-session" {
		t.Fatalf("topology envelope=%+v", topology)
	}
	if len(topology.ActiveAncestorPath) != 4 || topology.ActiveAncestorPath[len(topology.ActiveAncestorPath)-1] != topology.CurrentNodeID {
		t.Fatalf("active node path=%v current=%q", topology.ActiveAncestorPath, topology.CurrentNodeID)
	}
	if node := worldlineTopologyNodeByTurn(t, topology, "current-session", 4); !node.Current || node.Selected || topology.CurrentNodeID != node.NodeID {
		t.Fatalf("current node=%+v", node)
	}
	if node := worldlineTopologyNodeByTurn(t, topology, "selected-session", 3); node.Current || !node.Selected || topology.SelectedNodeID != node.NodeID {
		t.Fatalf("selected node=%+v", node)
	}
}

func TestTimelineViewReturnsUnavailableTopologyWithoutInventingGraphOnReadError(t *testing.T) {
	fake := &presentationTopologyStore{Store: store.NewNoopStore(), err: errors.New("database read failed")}
	srv := NewServer(config.Default())
	srv.Store = fake
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/timeline?sessionId=selected-session&currentSessionId=current-session&view=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response timelineViewResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	topology := response.WorldlineTopology
	if topology.State != "unavailable" || topology.Reason != "topology_snapshot_read_unavailable" || len(topology.Nodes) != 0 || len(topology.Edges) != 0 {
		t.Fatalf("read error invented topology: %+v", topology)
	}
}
