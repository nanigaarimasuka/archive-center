package httpapi

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"unicode"

	"github.com/risulongmemory/archive-center-go/internal/store"
)

// refAttachCurrentStateContext is prepareTurnAttachCurrentStateContext before
// its attachments were collected and multi-word names indexed by word.
func refAttachCurrentStateContext(out *prepareTurnInjectionAssembly, values []store.StatusCurrentValue, clock map[string]any) {
	// Compile only original source text once. Attached readings cannot recursively
	// introduce another subject, and large state registries do not re-tokenize it.
	sourceTerms := make([][]string, len(out.PriorityFactSeeds))
	sourcePhrases := make([]string, len(out.PriorityFactSeeds))
	wordBreak := func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '_' && r != '-' }
	for i, seed := range out.PriorityFactSeeds {
		text := seed.Fact.Text
		if seed.Fact.Reading != nil {
			for _, part := range seed.Fact.Reading.Parts {
				if !strings.HasPrefix(part.Key, "@") {
					text += "\n" + part.Value
				}
			}
		}
		sourceTerms[i] = prepareTurnRecallTerms(text)
		sourcePhrases[i] = " " + strings.Join(strings.FieldsFunc(strings.ToLower(text), wordBreak), " ") + " "
	}
	// Index the sources once: which facts contain each distinct token, and
	// which tokens could match a subject name. Each state then finds the
	// facts that mention it directly instead of scanning every fact's tokens.
	index := newPrepareTurnStateMentionIndex(sourceTerms)
	// Each matched fact gets one private reading copy in this pass. Later states
	// append to that copy instead of re-copying every earlier part per state.
	owned := map[int]*prepareTurnMemoryContext{}
	// A fact's entity key does not depend on the state; group facts by it once.
	var factsByEntityKey map[string][]int
	factStamp := make([]int, len(out.PriorityFactSeeds))
	viewIndex := 0
	for _, view := range narrativeCurrentStateViews(values) {
		switch view.Scope {
		case "belief", "rumor", "secret":
			continue // Same public-current projection boundary as lifecycle readings.
		}
		if view.Slot == "goal_status" {
			continue // Commitments retain their explicit lifecycle-key owner.
		}
		subjectKey := prepareTurnPriorityEntityKey(view.Subject, out.PriorityEntityAliases)
		viewIndex++
		subjectWords := strings.FieldsFunc(strings.ToLower(view.Subject), wordBreak)
		subjectPhrase := " " + strings.Join(subjectWords, " ") + " "
		// Facts that mention the subject, in fact order: the same entity key,
		// a whole source token naming it (Latin name boundaries and Korean
		// inflection as before), or a multi-word name phrase.
		if subjectKey != "" && factsByEntityKey == nil {
			factsByEntityKey = map[string][]int{}
			for i := range out.PriorityFactSeeds {
				if out.PriorityFactSeeds[i].SourceTable == "character_states" {
					continue
				}
				key := prepareTurnPriorityEntityKey(out.PriorityFactSeeds[i].Fact.EntitySurface, out.PriorityEntityAliases)
				factsByEntityKey[key] = append(factsByEntityKey[key], i)
			}
		}
		var mentioning []int
		mark := func(i int) {
			if factStamp[i] != viewIndex {
				factStamp[i] = viewIndex
				mentioning = append(mentioning, i)
			}
		}
		if subjectKey != "" {
			for _, i := range factsByEntityKey[subjectKey] {
				mark(i)
			}
		}
		for _, id := range index.matchingTokens(view.Subject) {
			for _, i := range index.factsByToken[id] {
				mark(i)
			}
		}
		if len(subjectWords) > 1 {
			for i := range sourcePhrases {
				if strings.Contains(sourcePhrases[i], subjectPhrase) {
					mark(i)
				}
			}
		}
		if len(mentioning) == 0 {
			continue
		}
		sort.Ints(mentioning)
		origin, evidence := prepareTurnCurrentStateReadingOrigin(view.Value)
		prefix := fmt.Sprintf("@current/%s/%s/%s", view.Value.OwnerScope, view.Value.OwnerID, view.Slot)
		observation := "source turn unknown"
		if origin.SourceTurn > 0 {
			observation = fmt.Sprintf("source turn %d", origin.SourceTurn)
		}
		parts := []prepareTurnMemoryPart{{Key: prefix, Label: fmt.Sprintf("linked stored state [%s; status_current_values:%d]", observation, view.Value.ID), DeliveryLabel: fmt.Sprintf("linked stored state [%s]", observation), Value: view.Subject + " · " + view.Slot + ": " + view.Current}}
		if excerpt := stringFromMap(evidence, "evidence_excerpt"); excerpt != "" {
			parts = append(parts, prepareTurnMemoryPart{Key: prefix + "/evidence", Label: "state evidence", Value: excerpt})
		}
		for _, field := range []string{"observed_at", "occurrence_time", "effective_time", "validity"} {
			if value, exists := view.Payload[field]; exists {
				evidence[field] = value
			}
		}
		for _, field := range []string{"source_revision", "direct_evidence_ids", "observed_at", "occurrence_time", "effective_time", "validity", "repair_source_revision", "repair_recorded_turn"} {
			if value := evidence[field]; value != nil {
				parts = append(parts, prepareTurnMemoryPart{Key: prefix + "/" + field, Label: "state " + field, Value: prepareTurnPriorityScalarText(value)})
			}
		}
		if temporal := prepareTurnSourceTemporalContext(evidence, view.Payload); len(temporal) > 0 {
			parts = append(parts, prepareTurnMemoryPart{Key: prefix + "/time", Label: "state time (read only)", Value: mustCompactJSON(buildStoryTimeReading(temporal, clock))})
		}
		for _, i := range mentioning {
			if out.PriorityFactSeeds[i].SourceTable == "character_states" {
				continue // Field-linked current readings already own these projections.
			}
			fact := &out.PriorityFactSeeds[i].Fact
			reading := owned[i]
			if reading == nil {
				reading = &prepareTurnMemoryContext{Path: fact.SourcePath, Parts: []prepareTurnMemoryPart{{Key: fact.SourcePath, Value: fact.Text, FactTexts: []string{fact.Text}}}}
				if fact.Reading != nil {
					*reading = *fact.Reading
					reading.Parts = append([]prepareTurnMemoryPart(nil), fact.Reading.Parts...)
				}
				reading.fingerprint = [32]byte{}
				owned[i] = reading
			}
			// A matching name remains discovery/scoring context. It does not make
			// every stored slot part of an explicitly typed assertion. Unknown
			// bindings retain the established reading (including indirect clues);
			// missing optional metadata never removes useful current context.
			linked := fact.StateSlot == "" || normalizeNarrativeStateSlot(fact.StateSlot) == view.Slot
			for _, field := range stringsFromAny(view.Payload["source_fields"]) {
				if fact.SourceFieldPath != "" && (fact.SourceFieldPath == field || strings.HasPrefix(fact.SourceFieldPath, strings.TrimRight(field, "/")+"/")) {
					linked = true
				}
			}
			for _, part := range parts {
				part.ReferenceOnly = !linked
				reading.Parts = append(reading.Parts, part)
			}
			fact.Reading = reading
		}
	}
}

func TestAttachCurrentStateContextMatchesReference(t *testing.T) {
	r := rand.New(rand.NewSource(53))
	names := []string{"Mira", "Rowan", "Brass Compass", "Old Brass Compass", "앨리스", "서울 역", "the gate", "Kael"}
	slots := []string{"inventory", "location", "condition", "goal_status"}
	scopes := []string{"", "", "public", "secret"}
	word := func() string {
		return []string{"Mira", "Rowan", "brass", "compass", "Brass Compass", "old", "앨리스가", "서울 역에서", "the", "gate", "Kael", "x", "-"}[r.Intn(13)]
	}
	sentence := func() string {
		parts := make([]string, 1+r.Intn(8))
		for i := range parts {
			parts[i] = word()
		}
		return strings.Join(parts, []string{" ", ", ", " - "}[r.Intn(3)])
	}
	attached := 0
	for c := 0; c < 400; c++ {
		seeds := make([]prepareTurnPriorityFactSeed, 1+r.Intn(12))
		for i := range seeds {
			fact := prepareTurnPriorityMemoryFact{Text: sentence(), SourcePath: fmt.Sprint("/f", i), EntitySurface: names[r.Intn(len(names))]}
			if r.Intn(3) == 0 {
				fact.StateSlot = slots[r.Intn(len(slots))]
				if r.Intn(2) == 0 {
					fact.StateSlot = " " + strings.ToUpper(fact.StateSlot) + " " // normalizes to the slot
				}
			}
			if r.Intn(3) == 0 {
				fact.Reading = &prepareTurnMemoryContext{Path: "/p", Parts: []prepareTurnMemoryPart{{Key: "/a", Value: sentence()}, {Key: "@b", Value: sentence()}}}
			}
			seeds[i] = prepareTurnPriorityFactSeed{SourceTable: []string{"memories", "world_rules", "character_states"}[r.Intn(3)], Fact: fact}
		}
		values := make([]store.StatusCurrentValue, r.Intn(8))
		for i := range values {
			payload := map[string]any{"subject": names[r.Intn(len(names))], "state_slot": slots[r.Intn(len(slots))], "value": sentence()}
			if scope := scopes[r.Intn(len(scopes))]; scope != "" {
				payload["scope"] = scope
			}
			if r.Intn(3) == 0 {
				payload["source_fields"] = []any{"/f1"}
			}
			values[i] = store.StatusCurrentValue{ID: int64(i + 1), StatusKey: narrativeStateStatusKey, WriteState: "current", OwnerScope: "entity", OwnerID: fmt.Sprint(i), SourceTurn: r.Intn(3), ValueJSON: mustCompactJSON(payload)}
			if r.Intn(2) == 0 {
				values[i].EvidenceJSON = mustCompactJSON(map[string]any{"evidence_excerpt": sentence(), "source_revision": "rev"})
			}
		}
		copySeeds := func() []prepareTurnPriorityFactSeed {
			out := append([]prepareTurnPriorityFactSeed(nil), seeds...)
			for i := range out {
				if out[i].Fact.Reading != nil {
					reading := *out[i].Fact.Reading
					reading.Parts = append([]prepareTurnMemoryPart(nil), reading.Parts...)
					out[i].Fact.Reading = &reading
				}
			}
			return out
		}
		got := prepareTurnInjectionAssembly{PriorityFactSeeds: copySeeds()}
		want := prepareTurnInjectionAssembly{PriorityFactSeeds: copySeeds()}
		// A plan is reused across seed sets: attach it to other seeds first.
		plan := newPrepareTurnCurrentStatePlan(values, got.PriorityEntityAliases, nil)
		other := prepareTurnInjectionAssembly{PriorityFactSeeds: copySeeds()[:len(seeds)/2]}
		prepareTurnAttachCurrentStatePlan(&other, plan)
		if r.Intn(2) == 0 {
			prepareTurnAttachCurrentStatePlan(&got, plan)
		} else {
			prepareTurnAttachCurrentStateContext(&got, values, nil)
		}
		refAttachCurrentStateContext(&want, values, nil)
		// Readings are not part of a seed's JSON; compare them directly.
		view := func(seeds []prepareTurnPriorityFactSeed) string {
			readings := make([]any, len(seeds))
			for i, seed := range seeds {
				if seed.Fact.Reading != nil {
					readings[i] = seed.Fact.Reading
				}
			}
			return mustCompactJSON(seeds) + mustCompactJSON(readings)
		}
		if a, b := view(got.PriorityFactSeeds), view(want.PriorityFactSeeds); a != b {
			t.Fatalf("case %d:\n got %s\nwant %s", c, a, b)
		}
		for _, seed := range got.PriorityFactSeeds {
			if seed.Fact.Reading != nil && len(seed.Fact.Reading.Parts) > 3 {
				attached++
			}
		}
	}
	if attached < 100 {
		t.Fatalf("only %d readings got state attached; the cases do not exercise attachment", attached)
	}
}
