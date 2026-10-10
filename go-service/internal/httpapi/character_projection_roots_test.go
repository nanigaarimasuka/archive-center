package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/risulongmemory/archive-center-go/internal/config"
	"github.com/risulongmemory/archive-center-go/internal/store"
)

// Surfaces join only the canonical row of their own identity root, also when
// several identities with several surfaces each are resolved together.
func TestCharacterProjectionAliasesFollowEachIdentityRoot(t *testing.T) {
	fake := newIdentityAliasLinkRecordingStore()
	srv := NewServer(config.Default())
	srv.Store = fake
	const sid = "sess-projection-roots"
	save := func(turn int, name, alias, evidence string) {
		t.Helper()
		first := map[string]any{"entities": map[string]any{"characters": []any{map[string]any{"name": name}}}}
		if result := srv.saveCriticExtractionArtifacts(context.Background(), sid, turn, first, name+" arrived.", completeTurnEmbeddingConfig{}, time.Unix(int64(100*turn), 0)); result.Errors != 0 {
			t.Fatalf("save %s: %#v", name, result.ErrorDetails)
		}
		second := map[string]any{"entities": map[string]any{"characters": []any{map[string]any{"name": name, "aliases": []any{alias}, "identity_evidence_excerpt": evidence}}}}
		if result := srv.saveCriticExtractionArtifacts(context.Background(), sid, turn+1, second, evidence, completeTurnEmbeddingConfig{}, time.Unix(int64(100*turn+50), 0)); result.Errors != 0 {
			t.Fatalf("save %s alias: %#v", name, result.ErrorDetails)
		}
	}
	save(1, "Hyun Jiyu", "Jiyu", "Jiyu smiled and watched his reaction.")
	save(3, "Park Mina", "Mina", "Mina laughed at the window.")
	projection := srv.canonicalCharacterReadProjection(context.Background(), sid, []store.CharacterState{
		{ID: 1, ChatSessionID: sid, CharacterName: "Hyun Jiyu", TurnIndex: 1},
		{ID: 2, ChatSessionID: sid, CharacterName: "Park Mina", TurnIndex: 3},
	}, nil)
	// The fake resolves no reviewed roots, so every surface is its own root:
	// no canonical row may take another identity's surfaces.
	if len(projection.StableIDs) != 2 {
		t.Fatalf("stable ids %#v", projection.StableIDs)
	}
	for canonical, foreign := range map[string][]string{"Hyun Jiyu": {"Park Mina", "Mina"}, "Park Mina": {"Hyun Jiyu", "Jiyu"}} {
		for _, value := range projection.Aliases[comparableEntityKey(canonical)] {
			for _, other := range foreign {
				if value == other {
					t.Fatalf("%s took %s's surface: %#v", canonical, other, projection.Aliases)
				}
			}
		}
	}
}
