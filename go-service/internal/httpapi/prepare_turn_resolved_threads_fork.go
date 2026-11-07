package httpapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/risulongmemory/archive-center-go/internal/store"
)

// A memory written while a plan or promise was open ("we meet for lunch
// tomorrow") keeps reading as pending after the story finished it. When the
// critic has closed the matching pending thread, the memory carries that
// outcome, so the main model reads both the old plan and that it is done.

// prepareTurnResolvedThreadMinMatch is how much of a thread's description a
// memory must share to be read as being about it, when no lifecycle key
// links them: two of its four most distinctive terms.
const prepareTurnResolvedThreadMinMatch = 0.5

// prepareTurnResolvedThreads reads the session's closed threads, keeping those
// closed before the turn being prepared (a regenerated turn must not see its
// own future). A failed read only leaves the outcomes out.
func (s *Server) prepareTurnResolvedThreads(ctx context.Context, sid string, currentTurn int) []store.PendingThread {
	if s == nil || s.Store == nil || sid == "" {
		return nil
	}
	threads, err := s.Store.ListPendingThreads(ctx, sid, "resolved")
	if err != nil {
		return nil
	}
	kept := threads[:0:0]
	for _, thread := range threads {
		if thread.ResolvedTurn > 0 && !thread.Suppressed && (currentTurn <= 0 || thread.ResolvedTurn < currentTurn) {
			kept = append(kept, thread)
		}
	}
	return kept
}

// prepareTurnAttachResolvedThreadContext adds the later outcome to each event
// memory written while a closed thread was still open and about that thread.
func prepareTurnAttachResolvedThreadContext(out *prepareTurnInjectionAssembly, threads []store.PendingThread) {
	if len(threads) == 0 {
		return
	}
	analyzed := map[string]prepareTurnPriorityLexicalText{}
	readText := func(text string) prepareTurnPriorityLexicalText {
		if cached, ok := analyzed[text]; ok {
			return cached
		}
		value := prepareTurnPriorityAnalyzeText(text)
		analyzed[text] = value
		return value
	}
	type closedThread struct {
		from, resolved int
		key            string
		match          func(string) float64
		part           prepareTurnMemoryPart
	}
	closed := make([]closedThread, 0, len(threads))
	for _, thread := range threads {
		description := strings.TrimSpace(thread.Description)
		if description == "" {
			description = strings.TrimSpace(thread.Title)
		}
		if description == "" {
			continue
		}
		from := thread.CreatedTurn
		if from <= 0 {
			from = thread.SourceTurn
		}
		outcome := strings.TrimSpace(thread.ResolutionNote)
		if outcome == "" {
			outcome = description
		}
		closed = append(closed, closedThread{
			from: from, resolved: thread.ResolvedTurn,
			key:   normalizeNarrativeLifecycleKey(stringFromMap(parseJSONMap(thread.HookMetadataJSON), "lifecycle_key")),
			match: prepareTurnPriorityRelevanceScorer(nil, description, readText),
			part: prepareTurnMemoryPart{
				Key:           fmt.Sprintf("@lifecycle/resolved_thread/%d", thread.ID),
				Label:         fmt.Sprintf("later outcome [pending_threads:%d; resolved turn %d]", thread.ID, thread.ResolvedTurn),
				DeliveryLabel: fmt.Sprintf("later outcome [resolved at turn %d; no longer pending]", thread.ResolvedTurn),
				Value:         outcome,
			},
		})
	}
	for i := range out.PriorityFactSeeds {
		seed := &out.PriorityFactSeeds[i]
		turn := seed.SourceTurn
		if !prepareTurnOverflowFloorSources[seed.SourceTable] || turn <= 0 {
			continue
		}
		fact := &seed.Fact
		parts := []prepareTurnMemoryPart{}
		for _, thread := range closed {
			if turn >= thread.resolved || (thread.from > 0 && turn < thread.from) {
				continue
			}
			if thread.key == "" || normalizeNarrativeLifecycleKey(fact.LifecycleKey) != thread.key {
				if thread.match(fact.Text) < prepareTurnResolvedThreadMinMatch {
					continue
				}
			}
			parts = append(parts, thread.part)
		}
		if len(parts) == 0 {
			continue
		}
		reading := prepareTurnMemoryContext{Path: fact.SourcePath, Parts: []prepareTurnMemoryPart{{Key: fact.SourcePath, Value: fact.Text, FactTexts: []string{fact.Text}}}}
		if fact.Reading != nil {
			reading = *fact.Reading
			reading.Parts = append([]prepareTurnMemoryPart(nil), fact.Reading.Parts...)
		}
		reading.fingerprint = [32]byte{}
		reading.Parts = append(reading.Parts, parts...)
		fact.Reading = &reading
	}
}
