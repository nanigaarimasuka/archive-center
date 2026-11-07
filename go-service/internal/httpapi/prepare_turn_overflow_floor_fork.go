package httpapi

// prepareTurnOverflowMinRelevance is the relevance an item past each group's
// core turn needs before it can take budget. The core items still go in as
// before; this only stops the overflow from filling the budget with memories
// that barely match. It is the blended current/context relevance: a memory
// sharing one distinctive term with the current input and two with a recent
// turn reaches it, one matching a recent turn only partly does not.
// The setting (memoryOverflowMinRelevance, 0 turns it off) replaces it.
const prepareTurnOverflowMinRelevanceDefault = 0.3

var prepareTurnOverflowMinRelevance = prepareTurnOverflowMinRelevanceDefault

// prepareTurnOverflowFloor is the configured floor, or the default.
func prepareTurnOverflowFloor(out *prepareTurnInjectionAssembly) float64 {
	if out == nil || out.overflowMinRelevance == nil {
		return prepareTurnOverflowMinRelevance
	}
	return min(max(*out.overflowMinRelevance, 0), 1)
}

// prepareTurnOverflowSummaryRelevance gives each turn summary the best
// relevance among the facts it was built from.
func prepareTurnOverflowSummaryRelevance(resolved []prepareTurnPriorityMemoryCandidate, summaries []prepareTurnPriorityTurnSummaryCandidate) []float64 {
	byFact := make(map[string]float64, len(resolved))
	for _, candidate := range resolved {
		byFact[candidate.CanonicalFactID] = candidate.Relevance
	}
	out := make([]float64, len(summaries))
	for index, summary := range summaries {
		for _, id := range summary.MemberFactIDs {
			out[index] = max(out[index], byFact[id])
		}
		if summary.SourceVectorSimilarityObserved {
			out[index] = max(out[index], prepareTurnPriorityNormalizeScore(summary.SourceVectorSimilarity))
		}
	}
	return out
}

// prepareTurnOverflowFloorSources are the event memories the floor applies to.
// State (world rules, character states, open threads, storylines) describes
// the present whether or not the input names it, so it keeps filling.
var prepareTurnOverflowFloorSources = map[string]bool{"memories": true, "precise_memory_units": true}

// prepareTurnOverflowBelowFloor says whether an overflow item stays out. Items
// a preprocessing role picked keep its choice, and body readings have their own
// budget.
func prepareTurnOverflowBelowFloor(out *prepareTurnInjectionAssembly, lane, budgetLane, sourceTable string, relevance float64) bool {
	if !prepareTurnOverflowFloorSources[sourceTable] || budgetLane == "body_tracking" || out.Preprocessing.usesAI(lane) || out.Preprocessing.usesJev(lane) {
		return false
	}
	return relevance < prepareTurnOverflowFloor(out)
}
