package httpapi

import (
	"context"
	"testing"

	"github.com/risulongmemory/archive-center-go/internal/config"
	"github.com/risulongmemory/archive-center-go/internal/store"
	"github.com/risulongmemory/archive-center-go/internal/vector"
)

// A revision is active per session: the same revision text can be active in
// one session and not in another, and a document without a session is
// checked in the request's session.
func TestSourceRevisionFilterChecksEachSession(t *testing.T) {
	lifecycleStore := &prepareRevisionFilterStore{
		Store:  store.NewNoopStore(),
		active: map[string]bool{"session-a:rev": true, "fallback:rev2": true},
	}
	server := NewServer(config.Default())
	server.Store = lifecycleStore
	docs := []vector.VectorDocument{
		{ID: "1", ChatSessionID: "session-a", Metadata: map[string]any{"source_revision": "rev"}},
		{ID: "2", ChatSessionID: "session-b", Metadata: map[string]any{"source_revision": "rev"}},
		{ID: "3", Metadata: map[string]any{"source_revision": "rev2"}},
		{ID: "4", ChatSessionID: "session-a", Metadata: map[string]any{"source_revision": "rev2"}},
		{ID: "5", ChatSessionID: "session-a", Metadata: map[string]any{"source_revision": "rev"}},
	}
	kept, trace := server.filterPrepareTurnActiveSourceRevisionVectors(context.Background(), "fallback", docs)
	ids := []string{}
	for _, doc := range kept {
		ids = append(ids, doc.ID)
	}
	if len(ids) != 3 || ids[0] != "1" || ids[1] != "3" || ids[2] != "5" {
		t.Fatalf("kept %v, want [1 3 5] (trace %v)", ids, trace)
	}
	if len(lifecycleStore.checks) != 4 {
		t.Fatalf("checked %v, want each session and revision once", lifecycleStore.checks)
	}
	for key, count := range lifecycleStore.checks {
		if count != 1 {
			t.Fatalf("%s checked %d times", key, count)
		}
	}
}
