package httpapi

import (
	"math/rand"
	"reflect"
	"testing"
)

func TestKeptFormDeliveryMatchesDirect(t *testing.T) {
	r := rand.New(rand.NewSource(83))
	empty, other := "", "delivered"
	for c := 0; c < 3000; c++ {
		parts := make([]prepareTurnMemoryFormPart, r.Intn(6))
		for i := range parts {
			parts[i] = prepareTurnMemoryFormPart{Key: []string{"/a", "/b", "@current/x"}[r.Intn(3)], Text: []string{"one", "two", "delivered"}[r.Intn(3)]}
			switch r.Intn(4) {
			case 0:
				parts[i].DeliveryText = &empty
			case 1:
				parts[i].DeliveryText = &other
			}
		}
		form := &prepareTurnMemoryForm{Parts: parts}
		form.kept = &prepareTurnKeptForm{source: form.Parts}
		want := prepareTurnMemoryDeliveryParts(parts)
		got, distinct, ok := form.keptDelivery()
		if !ok || !reflect.DeepEqual(got, want) || distinct != prepareTurnMemoryPartsDistinct(want) {
			t.Fatalf("case %d: %v %v %v, want %v %v", c, got, distinct, ok, want, prepareTurnMemoryPartsDistinct(want))
		}
		// A copy whose parts were replaced does not use the kept parts.
		copied := *form
		copied.Parts = append(append([]prepareTurnMemoryFormPart(nil), form.Parts...), prepareTurnMemoryFormPart{Key: "/extra", Text: "extra"})
		if _, _, ok := copied.keptDelivery(); ok {
			t.Fatalf("case %d: a copy with other parts used the kept delivery parts", c)
		}
	}
	if _, _, ok := (&prepareTurnMemoryForm{}).keptDelivery(); ok {
		t.Fatal("a form without kept delivery parts reported them")
	}
}
