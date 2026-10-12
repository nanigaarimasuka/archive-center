package httpapi

import (
	"encoding/json"
	"github.com/risulongmemory/archive-center-go/internal/config"
	"github.com/risulongmemory/archive-center-go/internal/store"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestTimelinePagesNeverSplitATurn(t *testing.T) {
	now := time.Now()
	fake := &memoryFakeStore{
		chatLogs: []store.ChatLog{
			{ID: 10, ChatSessionID: "sess-split", TurnIndex: 3, Role: "assistant", Content: "three", CreatedAt: now},
			{ID: 9, ChatSessionID: "sess-split", TurnIndex: 3, Role: "user", Content: "three", CreatedAt: now},
			{ID: 8, ChatSessionID: "sess-split", TurnIndex: 2, Role: "assistant", Content: "two", CreatedAt: now},
			{ID: 7, ChatSessionID: "sess-split", TurnIndex: 2, Role: "user", Content: "two", CreatedAt: now},
			{ID: 6, ChatSessionID: "sess-split", TurnIndex: 1, Role: "user", Content: "one", CreatedAt: now},
		},
	}
	srv := NewServer(config.Default())
	srv.Store = fake
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	// Reading page by page, the way the plugin's session normalize does,
	// returns every item even when a page limit falls inside a turn.
	got := map[int]int{}
	before := 0
	for page := 0; page < 10; page++ {
		url := "/timeline?sessionId=sess-split&limit=3"
		if before > 0 {
			url += "&beforeTurn=" + strconv.Itoa(before)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		var resp struct {
			Items []struct {
				TurnIndex int `json:"turn_index"`
			} `json:"items"`
			Meta struct {
				NextBeforeTurn int `json:"next_before_turn"`
			} `json:"meta"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &resp) != nil {
			t.Fatalf("page %d: %d %s", page, rec.Code, rec.Body.String())
		}
		for _, item := range resp.Items {
			got[item.TurnIndex]++
		}
		if resp.Meta.NextBeforeTurn == 0 {
			break
		}
		before = resp.Meta.NextBeforeTurn
	}
	if got[3] != 2 || got[2] != 2 || got[1] != 1 {
		t.Fatalf("items per turn = %v, want 3:2 2:2 1:1", got)
	}
}
