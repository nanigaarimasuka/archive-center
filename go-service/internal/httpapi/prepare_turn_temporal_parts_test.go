package httpapi

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// Temporal parts kept by context contents equal the ones built for each fact.
func TestTemporalPartsKeptByContentsMatchBuilt(t *testing.T) {
	r := rand.New(rand.NewSource(79))
	clock := map[string]any{"absolute": map[string]any{"date": "2002-06-04", "time": "13:00"}, "turn": 12}
	value := func() any {
		switch r.Intn(6) {
		case 0:
			return 3
		case 1:
			return 3.0
		case 2:
			return "3"
		case 3:
			return map[string]any{"date": "2002-06-0" + fmt.Sprint(1+r.Intn(3)), "time": "09:00"}
		case 4:
			return []any{"x", 2.0}
		default:
			return nil
		}
	}
	context := func() map[string]any {
		out := map[string]any{}
		for _, key := range []string{"observed_at", "occurrence_time", "relative_expression", "relative"} {
			if r.Intn(2) == 0 {
				out[key] = value()
			}
		}
		return out
	}
	kept := &prepareTurnTemporalParts{}
	for c := 0; c < 300; c++ {
		seeds := make([]prepareTurnPriorityFactSeed, 1+r.Intn(5))
		for i := range seeds {
			seeds[i].Fact = prepareTurnPriorityMemoryFact{Text: fmt.Sprint("fact ", i), SourcePath: "/f", TemporalContext: context()}
		}
		a := prepareTurnInjectionAssembly{PriorityFactSeeds: append([]prepareTurnPriorityFactSeed(nil), seeds...)}
		b := prepareTurnInjectionAssembly{PriorityFactSeeds: append([]prepareTurnPriorityFactSeed(nil), seeds...)}
		prepareTurnAttachTemporalContextWith(&a, clock, kept)
		prepareTurnAttachTemporalContext(&b, clock)
		for i := range seeds {
			got, want := a.PriorityFactSeeds[i].Fact.Reading, b.PriorityFactSeeds[i].Fact.Reading
			if (got == nil) != (want == nil) || (got != nil && mustCompactJSON(got) != mustCompactJSON(want)) {
				t.Fatalf("case %d fact %d: %s, want %s", c, i, mustCompactJSON(got), mustCompactJSON(want))
			}
		}
	}
	var intKey, floatKey strings.Builder
	prepareTurnWriteTypedKey(&intKey, map[string]any{"a": 1})
	prepareTurnWriteTypedKey(&floatKey, map[string]any{"a": 1.0})
	if intKey.String() == floatKey.String() {
		t.Fatalf("int and float values share a key: %s", intKey.String())
	}
}
