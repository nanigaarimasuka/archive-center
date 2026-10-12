package httpapi

import (
	"context"
	"encoding/json"
	"github.com/risulongmemory/archive-center-go/internal/config"
	"github.com/risulongmemory/archive-center-go/internal/store"
	"testing"
	"time"
)

// staleCurrentClockStore hides a newer current clock from the read and
// refuses to replace it at the write, as while a repair has replaced the
// source of a later edited turn and not yet rebuilt it.
type staleCurrentClockStore struct {
	*turnRecordingStore
}

func (s staleCurrentClockStore) ApplyReversibleStatusTransition(ctx context.Context, transition store.ReversibleStatusTransition) (store.ReversibleStatusTransitionResult, error) {
	if transition.CurrentValue != nil {
		return store.ReversibleStatusTransitionResult{}, store.ErrStatusProjectionStale
	}
	return s.turnRecordingStore.ApplyReversibleStatusTransition(ctx, transition)
}

func TestStoryClockFoundNewerAtWriteIsRecordedAsHistory(t *testing.T) {
	fake := &turnRecordingStore{}
	server := NewServer(config.Default())
	server.Store = staleCurrentClockStore{fake}
	excerpt := "The date was 1423-04-12."
	proposal := storyClockProposal("absolute", "current", "exact", excerpt)
	proposal["absolute"] = map[string]any{"date": "1423-04-12"}
	result := artifactSaveResult{}
	server.saveStoryClockFromExtraction(
		acceptedStoryClockContext("revision-599", "logical-story-turn", "generation-599"),
		"story-session", 599, map[string]any{"story_clock": proposal},
		excerpt+" The scene continued.", storyClockEvidence(599, 599, excerpt), time.Unix(599, 0), &result,
	)
	if result.Errors != 0 {
		t.Fatalf("a newer clock found at the write failed the turn: %#v", result.ErrorDetails)
	}
	if len(fake.savedStatusCurrent) != 0 || len(fake.savedStatusEvents) != 1 {
		t.Fatalf("current=%#v events=%#v, want one history event", fake.savedStatusCurrent, fake.savedStatusEvents)
	}
	event := fake.savedStatusEvents[0]
	evidence := map[string]any{}
	_ = json.Unmarshal([]byte(event.EvidenceJSON), &evidence)
	if event.EventKind != "observation_recorded" || event.EventState != "recorded" || event.OwnerID != "source:revision-599" ||
		evidence["current_projection"] != false || evidence["resolution_status"] != "older_turn_historical_only" {
		t.Fatalf("event = %#v evidence = %v, want an older-turn history record", event, evidence)
	}
}
