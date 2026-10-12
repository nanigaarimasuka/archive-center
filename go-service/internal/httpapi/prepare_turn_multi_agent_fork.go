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

// multiAgentInputItem is a candidate item of a role's input with what
// choosing it reads: its id and its text's rune count. A fact's item map is
// built by get, once it is chosen.
type multiAgentInputItem struct {
	id    string
	chars int
	item  map[string]any
	build func() map[string]any
}

func (m multiAgentInputItem) get() map[string]any {
	if m.item == nil && m.build != nil {
		return m.build()
	}
	return m.item
}

func multiAgentEagerItem(item map[string]any) multiAgentInputItem {
	return multiAgentInputItem{id: extractionStringFromAny(item["id"]), chars: len([]rune(extractionStringFromAny(item["text"]))), item: item}
}

// multiAgentLazyFactItem is prepareTurnMemoryModelCandidate(c, refs), built
// when chosen. Its text is prepareTurnMemoryReadingText(c), whose rune count
// a reading form already holds as Chars.
func multiAgentLazyFactItem(c prepareTurnPriorityMemoryCandidate, refs map[string]string) multiAgentInputItem {
	chars := 0
	if c.Minimum != nil {
		chars = c.Minimum.Chars
	} else {
		chars = len([]rune(prepareTurnMemoryReadingText(c)))
	}
	return multiAgentInputItem{id: c.CanonicalFactID, chars: chars, build: func() map[string]any {
		return prepareTurnMemoryModelCandidate(c, refs)
	}}
}
