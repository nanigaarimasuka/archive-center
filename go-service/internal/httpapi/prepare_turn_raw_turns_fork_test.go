package httpapi

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/risulongmemory/archive-center-go/internal/store"
)

func rawTurnLogs(turns int) []store.ChatLog {
	logs := []store.ChatLog{}
	for turn := 1; turn <= turns; turn++ {
		logs = append(logs,
			store.ChatLog{TurnIndex: turn, Role: "user", Content: "user line " + strings.Repeat("u", turn) + " asks about the harbor at turn " + strconv.Itoa(turn)},
			store.ChatLog{TurnIndex: turn, Role: "assistant", Content: "Long reply for turn " + strconv.Itoa(turn) + ". " + strings.Repeat("The tide rolled in over the stones and the lanterns flickered. ", 4)},
		)
	}
	return logs
}

func TestRawTurnsInRequestCountsTheNewestTurnsInThePrompt(t *testing.T) {
	logs := rawTurnLogs(6)
	messages := []map[string]any{{"role": "system", "content": "You are a narrator."}}
	// Turns 4-6 are in the prompt; a script changed the ends of each reply.
	for _, log := range logs[6:] {
		messages = append(messages, map[string]any{"role": log.Role, "content": "<p>" + log.Content + "</p>\n\n"})
	}
	messages = append(messages, map[string]any{"role": "user", "content": "now the current input"})
	if got := prepareTurnRawTurnsInRequestFrom(logs, messages); got != 4 {
		t.Fatalf("from = %d, want 4", got)
	}
}

func TestRawTurnsInRequestNeedsTheNewestTurn(t *testing.T) {
	logs := rawTurnLogs(6)
	// An old turn quoted in the prompt, the newest one missing.
	messages := []map[string]any{{"role": "system", "content": "Example: " + logs[3].Content}}
	if got := prepareTurnRawTurnsInRequestFrom(logs, messages); got != 0 {
		t.Fatalf("from = %d, want 0", got)
	}
	if got := prepareTurnRawTurnsInRequestFrom(nil, messages); got != 0 {
		t.Fatalf("no logs: from = %d", got)
	}
}

func TestRawTurnsInRequestReadsMessagePartLists(t *testing.T) {
	logs := rawTurnLogs(2)
	messages := []map[string]any{{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": logs[3].Content}}}}
	if got := prepareTurnRawTurnsInRequestFrom(logs, messages); got != 2 {
		t.Fatalf("from = %d, want 2", got)
	}
}

func TestRawTurnsInRequestLeaveDeliveryButKeepState(t *testing.T) {
	out := &prepareTurnInjectionAssembly{rawTurnsInRequestFrom: 8, Counts: map[string]any{}}
	facts := []prepareTurnPriorityMemoryCandidate{
		{CanonicalFactID: "old", SourceTable: "memories", SourceTurn: 7},
		{CanonicalFactID: "recent", SourceTable: "memories", SourceTurn: 8},
		{CanonicalFactID: "unit", SourceTable: "precise_memory_units", SourceTurn: 9},
		{CanonicalFactID: "rule", SourceTable: "world_rules", SourceTurn: 9},
		{CanonicalFactID: "thread", SourceTable: "pending_threads", SourceTurn: 8},
	}
	summaries := []prepareTurnPriorityTurnSummaryCandidate{{SummaryID: "s7", SourceTurn: 7}, {SummaryID: "s9", SourceTurn: 9}}
	keptFacts, keptSummaries := prepareTurnDropRawTurnsInRequest(out, facts, summaries)
	ids := []string{}
	for _, fact := range keptFacts {
		ids = append(ids, fact.CanonicalFactID)
	}
	if !reflect.DeepEqual(ids, []string{"old", "rule", "thread"}) || len(keptSummaries) != 1 || keptSummaries[0].SummaryID != "s7" {
		t.Fatalf("kept facts %v, summaries %+v", ids, keptSummaries)
	}
	if out.Counts["raw_turn_in_request_excluded"] != 3 || out.Counts["raw_turns_in_request_from"] != 8 || len(facts) != 5 {
		t.Fatalf("counts %+v, input changed to %d", out.Counts, len(facts))
	}
	evidence := prepareTurnDropRawTurnEvidence(out, []string{"- [source turn 9] latest quote", "- [vector, turn 3] old quote", "- [vector, turn 8] recent quote", "- correction note"})
	if !reflect.DeepEqual(evidence, []string{"- [vector, turn 3] old quote", "- correction note"}) {
		t.Fatalf("evidence = %v", evidence)
	}
	off := &prepareTurnInjectionAssembly{}
	if kept, _ := prepareTurnDropRawTurnsInRequest(off, facts, summaries); len(kept) != 5 {
		t.Fatal("dropped without a known range")
	}
}
