package httpapi

import (
	"fmt"
	"strings"

	"github.com/risulongmemory/archive-center-go/internal/store"
)

// The critic may name earlier memories the latest turn made outdated (a plan
// that happened, a schedule that changed) in `superseded_memories`. The newer
// memory keeps that list in its stored extraction; reading it here lets each
// outdated memory carry a note that a later turn changed it. The old memory
// stays as history: only the reading beside it is added.

type prepareTurnSupersession struct {
	byID   int64
	turn   int
	reason string
}

// prepareTurnSupersessions maps each superseded memory id to the later turns
// that superseded it. Only a later memory can supersede an earlier one.
func prepareTurnSupersessions(memories []store.Memory) map[int64][]prepareTurnSupersession {
	turns := make(map[int64]int, len(memories))
	for _, memory := range memories {
		turns[memory.ID] = memory.TurnIndex
	}
	out := map[int64][]prepareTurnSupersession{}
	for _, memory := range memories {
		if !strings.Contains(memory.SummaryJSON, "superseded_memories") {
			continue
		}
		for _, raw := range sliceFromAny(parseJSONMap(memory.SummaryJSON)["superseded_memories"]) {
			item := mapFromAny(raw)
			target := int64(intFromAny(item["memory_id"], 0))
			targetTurn, known := turns[target]
			if target <= 0 || target == memory.ID || !known || targetTurn >= memory.TurnIndex {
				continue
			}
			out[target] = append(out[target], prepareTurnSupersession{
				byID: memory.ID, turn: memory.TurnIndex, reason: strings.TrimSpace(stringFromMap(item, "reason")),
			})
		}
	}
	return out
}

func prepareTurnAttachSupersededMemoryContext(out *prepareTurnInjectionAssembly, memories []store.Memory) {
	supersessions := prepareTurnSupersessions(memories)
	if len(supersessions) == 0 {
		return
	}
	for i := range out.PriorityFactSeeds {
		seed := &out.PriorityFactSeeds[i]
		id, _ := seed.SourceRowID.(int64)
		later := supersessions[id]
		if seed.SourceTable != "memories" || len(later) == 0 {
			continue
		}
		fact := &seed.Fact
		reading := prepareTurnMemoryContext{Path: fact.SourcePath, Parts: []prepareTurnMemoryPart{{Key: fact.SourcePath, Value: fact.Text, FactTexts: []string{fact.Text}}}}
		if fact.Reading != nil {
			reading = *fact.Reading
			reading.Parts = append([]prepareTurnMemoryPart(nil), fact.Reading.Parts...)
		}
		reading.fingerprint = [32]byte{}
		for _, s := range later {
			reason := s.reason
			if reason == "" {
				reason = "a later turn changed what this memory describes"
			}
			reading.Parts = append(reading.Parts, prepareTurnMemoryPart{
				Key:           fmt.Sprintf("@lifecycle/superseded/%d/%d", id, s.byID),
				Label:         fmt.Sprintf("later change [memories:%d; turn %d]", s.byID, s.turn),
				DeliveryLabel: fmt.Sprintf("later change [turn %d; this memory is out of date]", s.turn),
				Value:         reason,
			})
		}
		fact.Reading = &reading
	}
}
