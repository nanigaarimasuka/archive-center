package httpapi

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/risulongmemory/archive-center-go/internal/store"
)

// The main model already reads the newest turns as raw text in the request.
// Their memories and quoted evidence would say the same thing again, so they
// are left out of delivery. State (world rules, character states, open
// threads) stays: it says what is true now, not what happened.

// prepareTurnRawTurnProbeMargin is how much of each end of a stored message
// may differ in the request, where RisuAI scripts may wrap or trim it. The rest
// must appear as it is.
const prepareTurnRawTurnProbeMargin = 20

// prepareTurnRawTurnsInRequestFrom returns the first turn of the run of newest
// completed turns whose text is in the request, or 0 when the newest one is
// not. It walks back from the newest turn and stops at the first turn it
// cannot find, so an old turn quoted elsewhere in the prompt does not count.
func prepareTurnRawTurnsInRequestFrom(logs []store.ChatLog, messages []map[string]any) int {
	if len(logs) == 0 || len(messages) == 0 {
		return 0
	}
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		if text := prepareTurnRawTurnNormalize(prepareTurnRequestMessageText(message["content"])); text != "" {
			parts = append(parts, text)
		}
	}
	request := strings.Join(parts, "\n")
	byTurn := map[int][]string{}
	turns := []int{}
	for _, log := range logs {
		if log.TurnIndex <= 0 || strings.TrimSpace(log.Content) == "" {
			continue
		}
		if _, seen := byTurn[log.TurnIndex]; !seen {
			turns = append(turns, log.TurnIndex)
		}
		byTurn[log.TurnIndex] = append(byTurn[log.TurnIndex], log.Content)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(turns)))
	from := 0
	for _, turn := range turns {
		found := false
		for _, content := range byTurn[turn] {
			if probe := prepareTurnRawTurnProbe(content); probe != "" && strings.Contains(request, probe) {
				found = true
				break
			}
		}
		if !found {
			break
		}
		from = turn
	}
	return from
}

func prepareTurnRequestMessageText(content any) string {
	switch value := content.(type) {
	case string:
		return value
	case []any:
		texts := []string{}
		for _, part := range value {
			if item, ok := part.(map[string]any); ok {
				if text, ok := item["text"].(string); ok {
					texts = append(texts, text)
				}
			}
		}
		return strings.Join(texts, "\n")
	}
	return ""
}

var prepareTurnRawTurnSpace = regexp.MustCompile(`\s+`)

func prepareTurnRawTurnNormalize(text string) string {
	return strings.TrimSpace(prepareTurnRawTurnSpace.ReplaceAllString(text, " "))
}

func prepareTurnRawTurnProbe(content string) string {
	runes := []rune(prepareTurnRawTurnNormalize(content))
	if len(runes) <= 4*prepareTurnRawTurnProbeMargin {
		return string(runes)
	}
	return strings.TrimSpace(string(runes[prepareTurnRawTurnProbeMargin : len(runes)-prepareTurnRawTurnProbeMargin]))
}

// prepareTurnDropRawTurnsInRequest removes the event memories of turns that
// are in the request from the candidate pool, before preprocessing or delivery
// sees them.
func prepareTurnDropRawTurnsInRequest(out *prepareTurnInjectionAssembly, facts []prepareTurnPriorityMemoryCandidate, summaries []prepareTurnPriorityTurnSummaryCandidate) ([]prepareTurnPriorityMemoryCandidate, []prepareTurnPriorityTurnSummaryCandidate) {
	from := out.rawTurnsInRequestFrom
	if from <= 0 {
		return facts, summaries
	}
	dropped := 0
	keptFacts := facts[:0:0]
	for _, fact := range facts {
		if prepareTurnOverflowFloorSources[fact.SourceTable] && fact.SourceTurn >= from {
			dropped++
			continue
		}
		keptFacts = append(keptFacts, fact)
	}
	keptSummaries := summaries[:0:0]
	for _, summary := range summaries {
		if summary.SourceTurn >= from {
			dropped++
			continue
		}
		keptSummaries = append(keptSummaries, summary)
	}
	if out.Counts != nil {
		out.Counts["raw_turns_in_request_from"] = from
		out.Counts["raw_turn_in_request_excluded"] = dropped
	}
	return keptFacts, keptSummaries
}

var prepareTurnEvidenceTurnPattern = regexp.MustCompile(`^- \[(?:source turn |vector, turn )([0-9]+)[\],]`)

// prepareTurnDropRawTurnEvidence removes quoted evidence of turns that are in
// the request.
func prepareTurnDropRawTurnEvidence(out *prepareTurnInjectionAssembly, items []string) []string {
	if out.rawTurnsInRequestFrom <= 0 {
		return items
	}
	kept := items[:0:0]
	for _, item := range items {
		if match := prepareTurnEvidenceTurnPattern.FindStringSubmatch(item); match != nil {
			if turn, _ := strconv.Atoi(match[1]); turn >= out.rawTurnsInRequestFrom {
				continue
			}
		}
		kept = append(kept, item)
	}
	return kept
}
