package httpapi

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// partDigest equals the direct digest for copied, rebuilt and aliased parts,
// including a fact list whose shared backing array a later append overwrote.
func TestPreparationPartDigestMatchesDirectDigest(t *testing.T) {
	r := rand.New(rand.NewSource(17))
	words := []string{"", "a", "가", "long " + strings.Repeat("x", 200), "b:c", "\x1f"}
	pick := func() string { return words[r.Intn(len(words))] }
	for c := 0; c < 300; c++ {
		p := &prepareTurnRequestPreparation{partDigests: map[prepareTurnPartDigestKey][32]byte{}, partDigestsByStorage: map[uint64][]prepareTurnStoredPartDigest{}}
		var parts []prepareTurnMemoryPart
		for step := 0; step < 40; step++ {
			var part prepareTurnMemoryPart
			switch {
			case len(parts) > 0 && r.Intn(3) == 0:
				part = parts[r.Intn(len(parts))] // a copy sharing storage
				if r.Intn(2) == 0 {
					part.FactTexts = append(part.FactTexts, pick()) // may overwrite another copy's facts
				}
			case len(parts) > 0 && r.Intn(4) == 0:
				part = parts[r.Intn(len(parts))]
				part.Value = strings.Clone(part.Value) // equal content, new storage
			default:
				part = prepareTurnMemoryPart{Key: pick(), Label: pick(), Value: fmt.Sprint(pick(), r.Intn(3)), DeliveryLabel: pick(), ReferenceOnly: r.Intn(2) == 0}
				switch r.Intn(3) {
				case 0:
					part.FactTexts = make([]string, 0, 4)
				case 1:
					part.FactTexts = append(make([]string, 0, 4), pick())
				}
			}
			parts = append(parts, part)
			for _, check := range parts {
				if got, want := p.partDigest(check), prepareTurnMemoryPartDigest(check); got != want {
					t.Fatalf("case %d step %d: digest of %#v differs", c, step, check)
				}
			}
		}
	}
}

// A storage hash collision is confirmed by content and never returns another
// part's digest.
func TestPreparationPartDigestConfirmsCollidingStorage(t *testing.T) {
	p := &prepareTurnRequestPreparation{partDigests: map[prepareTurnPartDigestKey][32]byte{}, partDigestsByStorage: map[uint64][]prepareTurnStoredPartDigest{}}
	part := prepareTurnMemoryPart{Key: "k", Label: "l", Value: "v", FactTexts: []string{"f"}}
	other := prepareTurnMemoryPart{Key: "k", Label: "l", Value: "w", FactTexts: []string{"f"}}
	storage := prepareTurnPartStorageHash(part)
	p.partDigestsByStorage[storage] = []prepareTurnStoredPartDigest{{other, prepareTurnMemoryPartDigest(other)}}
	if p.partDigest(part) != prepareTurnMemoryPartDigest(part) {
		t.Fatal("colliding stored part's digest was returned")
	}
	if len(p.partDigestsByStorage[storage]) != 2 || p.partDigest(part) != prepareTurnMemoryPartDigest(part) {
		t.Fatal("part was not remembered next to the colliding one")
	}
}
