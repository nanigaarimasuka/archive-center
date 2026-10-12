package httpapi

import (
	"strings"
)

func prepareTurnPriorityLexicalTermOf(term string) prepareTurnPriorityLexicalTerm {
	run := []rune(term)
	v := prepareTurnPriorityLexicalTerm{value: term, nonASCII: len(run) >= 2 && prepareTurnContainsNonASCII(run)}
	if len(run) >= 3 && prepareTurnContainsNonASCII(run[:len(run)-1]) {
		v.shorter = string(run[:len(run)-1])
	}
	return v
}

// prepareTurnPrioritySurfaceMatcher is prepareTurnPrioritySurfaceMatches
// for one query, normalizing and tokenizing the query once.
func prepareTurnPrioritySurfaceMatcher(query string) func(surface string) bool {
	query = strings.TrimSpace(query)
	var queryNeedle string
	var queryTerms []string
	prepared := false
	return func(surface string) bool {
		surface = strings.TrimSpace(surface)
		if query == "" || surface == "" {
			return false
		}
		if !prepared {
			queryNeedle, queryTerms, prepared = normalizePrepareTurnEntityNeedle(query), prepareTurnRecallTerms(query), true
		}
		surfaceNeedle := normalizePrepareTurnEntityNeedle(surface)
		if strings.Contains(queryNeedle, surfaceNeedle) || strings.Contains(surfaceNeedle, queryNeedle) {
			return true
		}
		terms := prepareTurnDistinctiveRecallTerms(surface)
		if len(terms) == 0 {
			terms = prepareTurnRecallTerms(surface)
		}
		for _, term := range terms {
			for _, queryTerm := range queryTerms {
				if term == queryTerm || prepareTurnPriorityInflectedNonASCIIMatch(term, queryTerm) {
					return true
				}
			}
		}
		return false
	}
}
