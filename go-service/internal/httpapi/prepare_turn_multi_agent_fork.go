package httpapi

// The optional reader returns the same analysis as prepareTurnPriorityAnalyzeText
// (the request cache); it must not be shared with concurrent goroutines.
func multiAgentCurrentRelevanceByText(currentInput string, facts []prepareTurnPriorityMemoryCandidate, lexicalText func(string) prepareTurnPriorityLexicalText) map[string]float64 {
	texts := []string{}
	scores := make(map[string]float64, len(facts))
	add := func(text string) {
		if _, ok := scores[text]; !ok {
			scores[text] = 0
			texts = append(texts, text)
		}
	}
	for _, c := range facts {
		add(c.CompleteText)
		if c.Minimum != nil {
			add(c.Minimum.Meaning)
		}
	}
	newScorer := func() func(string) float64 { return prepareTurnPriorityRelevanceScorer(nil, currentInput) }
	if lexicalText != nil {
		// Analyze (and cache) each text once in order, as scoring did; the
		// concurrent scorers below then only read cached analyses.
		for _, text := range texts {
			lexicalText(text)
		}
		newScorer = func() func(string) float64 { return prepareTurnPriorityRelevanceScorer(nil, currentInput, lexicalText) }
	}
	for i, score := range prepareTurnParallelScores(texts, newScorer) {
		scores[texts[i]] = score
	}
	return scores
}
