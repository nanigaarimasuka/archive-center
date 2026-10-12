package httpapi

import (
	"encoding/json"
	"github.com/risulongmemory/archive-center-go/internal/store"
	"regexp"
	"sort"
	"strings"
)

// episodeMissingMemoryTurns lists the turns in a range that have chat logs
// but no memory. An episode built over such a turn would fill the gap with
// raw chat text, so the range has to wait until the critic has run for every
// turn. Sessions without any memory at all (no critic configured) are never
// blocked.
func episodeMissingMemoryTurns(sid string, fromTurn, toTurn int, logs []store.ChatLog, sessionMemories []store.Memory) []int {
	hasMemory := map[int]bool{}
	anyMemory := false
	for _, item := range sessionMemories {
		if item.ChatSessionID != sid || item.TurnIndex <= 0 {
			continue
		}
		anyMemory = true
		hasMemory[item.TurnIndex] = true
	}
	if !anyMemory {
		return nil
	}
	seen := map[int]bool{}
	missing := []int{}
	for _, log := range logs {
		if log.ChatSessionID != sid || log.TurnIndex < fromTurn || log.TurnIndex > toTurn || seen[log.TurnIndex] {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(log.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		seen[log.TurnIndex] = true
		if !hasMemory[log.TurnIndex] {
			missing = append(missing, log.TurnIndex)
		}
	}
	sort.Ints(missing)
	return missing
}

var markdownCommentLinePattern = regexp.MustCompile(`^\[[^\]]*\]:\s*#`)

// episodeMemoryCharacterNames lists the characters the range's memories name,
// in order of first appearance. The capitalized-word fallback cannot find
// names in Korean or Japanese text.
func episodeMemoryCharacterNames(memories []store.Memory) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] || len(out) >= 8 {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, mem := range memories {
		var parsed struct {
			Entities struct {
				Characters []struct {
					Name string `json:"name"`
				} `json:"characters"`
			} `json:"entities"`
			CharacterDeltas []struct {
				Name string `json:"name"`
			} `json:"character_deltas"`
		}
		if json.Unmarshal([]byte(mem.SummaryJSON), &parsed) != nil {
			continue
		}
		for _, character := range parsed.Entities.Characters {
			add(character.Name)
		}
		for _, delta := range parsed.CharacterDeltas {
			add(delta.Name)
		}
	}
	return out
}
