package httpapi

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

// The digest names each field of the seed's JSON; a new field must be added.
func TestSourceSeedDigestCoversEveryEncodedField(t *testing.T) {
	if got := reflect.TypeOf(prepareTurnPriorityFactSeed{}).NumField(); got != 26 {
		t.Fatalf("prepareTurnPriorityFactSeed has %d fields; update prepareTurnSourceSeedDigest and this count", got)
	}
	if got := reflect.TypeOf(prepareTurnPriorityMemoryFact{}).NumField(); got != 24 {
		t.Fatalf("prepareTurnPriorityMemoryFact has %d fields; update prepareTurnSourceSeedDigest and this count", got)
	}
}

func TestSourceSeedDigestHasTheJSONDigestEquality(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	pick := func(values ...string) string { return values[r.Intn(len(values))] }
	seed := func() prepareTurnPriorityFactSeed {
		s := prepareTurnPriorityFactSeed{
			SourceRef: pick("", "memories:1", "memories:2"), Lane: pick("event_recent", "character_objective"),
			SourceTable: pick("memories", "character_states"), Tier: pick("required", "auxiliary"),
			SourceOccurrence: pick("", "o1"), SourceTurn: r.Intn(3), FieldObservationTurn: r.Intn(2),
			Importance: []float64{0, 0.5, 1}[r.Intn(3)], ImportancePresent: r.Intn(2) == 0,
			ProjectionSource: pick("", "body_tracking:x"), ParentLineKey: pick("", "line"), SourceFactCount: r.Intn(2),
			// Query discoveries never change the key.
			SourceSelectionScore: r.Float64(), SourceSelectionScoreIsVector: r.Intn(2) == 0,
			RecallQueries: []any{pick("a", "b")}, SemanticUnitID: pick("u1", "u2"),
		}
		switch r.Intn(4) {
		case 0:
			s.SourceRowID = int64(r.Intn(2))
		case 1:
			s.SourceRowID = float64(r.Intn(2)) // same JSON as the int64
		case 2:
			s.SourceRowID = fmt.Sprint(r.Intn(2))
		}
		if r.Intn(2) == 0 {
			s.AllowedViewers = []string{}
		}
		if r.Intn(3) == 0 {
			s.AllowedViewers = []string{pick("a", "b")}
		}
		s.Fact = prepareTurnPriorityMemoryFact{
			Text: pick("The door was locked.", "문이 잠겼다.", "a<b&c"), EntitySurface: pick("", "Mira"),
			StateSlot: pick("", "mood"), Structured: r.Intn(2) == 0, MemoryRole: pick("", "identity_metadata"),
		}
		if r.Intn(3) == 0 {
			s.Fact.KnowledgeBoundaries = []map[string]any{{"scope": pick("x", "y"), "holders": []any{"a"}}}
		}
		if r.Intn(3) == 0 {
			s.Fact.TemporalContext = map[string]any{"observed_at": r.Intn(2)}
		}
		if r.Intn(4) == 0 {
			s.Fact.ExplicitRefs = []string{}
		}
		return s
	}
	seeds := make([]prepareTurnPriorityFactSeed, 400)
	for i := range seeds {
		seeds[i] = seed()
		if i > 0 && i%3 == 0 {
			// The same source seen again with other query discoveries.
			again := seeds[r.Intn(i)]
			again.SourceSelectionScore, again.RecallQueries, again.SemanticUnitID = r.Float64(), []any{"c"}, "u3"
			seeds[i] = again
		}
	}
	equalPairs := 0
	for i := range seeds {
		got, ok := prepareTurnSourceSeedDigest(seeds[i])
		if !ok {
			t.Fatalf("seed %d was not digested", i)
		}
		for j := range seeds[:i] {
			other, _ := prepareTurnSourceSeedDigest(seeds[j])
			sameJSON := prepareTurnSourceSeedJSONDigest(seeds[i]) == prepareTurnSourceSeedJSONDigest(seeds[j])
			if (got == other) != sameJSON {
				t.Fatalf("seeds %d and %d: digest equal=%v, JSON equal=%v", i, j, got == other, sameJSON)
			}
			if sameJSON {
				equalPairs++
			}
		}
	}
	if equalPairs == 0 {
		t.Fatal("no equal pairs were compared")
	}
	invalid := seed()
	invalid.Fact.Text = "bad \xff byte"
	if _, ok := prepareTurnSourceSeedDigest(invalid); ok {
		t.Fatal("invalid UTF-8 was digested; JSON replaces it, so the JSON digest must be used")
	}
}
