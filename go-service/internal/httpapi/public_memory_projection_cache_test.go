package httpapi

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/risulongmemory/archive-center-go/internal/store"
)

// refPublicMemoryFromCanonical is publicMemoryFromCanonical before its
// results were cached.
func refPublicMemoryFromCanonical(mem store.Memory) (store.Memory, bool) {
	projection := buildPublicMemoryProjection(parseJSONMap(mem.SummaryJSON), mem.Evidence)
	if !projection.Eligible {
		return store.Memory{}, false
	}
	out := mem
	out.SummaryJSON = mustCompactJSON(projection.Extraction)
	out.Evidence = mustCompactJSON(map[string]any{
		"evidence_excerpts": stringsFromAny(projection.Extraction["evidence_excerpts"]),
	})
	return out, true
}

func TestPublicMemoryProjectionCacheMatchesDirect(t *testing.T) {
	defer releasePublicMemoryProjectionCache()
	r := rand.New(rand.NewSource(67))
	summaries := []string{
		`{"turn_summary":"Mina returned the brass key.","evidence_excerpts":["Mina handed over the key."]}`,
		`{"turn_summary":"A secret vow.","visibility":"private","evidence_excerpts":["whispered"]}`,
		`{"narrative_events":[{"actor":"Kael","event":"left the guard","visibility":"public"}],"evidence_excerpts":["Kael left."]}`,
		`{"narrative_events":[{"actor":"Rin","event":"hid the map","visibility":"secret"}]}`,
		`not json`, ``,
		`{"turn_summary":"The gate opened.","knowledge_boundaries":[{"subject":"gate","unknown_to":["Mina"]}]}`,
		`{"turn_summary":"Jiyu met Mina.","characters":["Hyun Jiyu"],"aliases":["Jiyu"],"evidence_excerpts":["They met."]}`,
		`{"turn_summary":"A secret vow.","visibility":"private","characters":["Rin"],"aliases":["Vow keeper"]}`,
		`{"aliases":["Lone alias"],"characters":["Rin"]}`,
	}
	evidences := []string{``, `{"evidence_excerpts":["stored excerpt"]}`, `{"evidence_excerpts":[]}`}
	for c := 0; c < 400; c++ {
		mem := store.Memory{ID: int64(c), ChatSessionID: "s", TurnIndex: r.Intn(9), SummaryJSON: summaries[r.Intn(len(summaries))], Evidence: evidences[r.Intn(len(evidences))]}
		if r.Intn(2) == 0 {
			mem.SummaryJSON = strings.Clone(mem.SummaryJSON) // same contents, new storage
		}
		got, gotOK := publicMemoryFromCanonical(mem)
		want, wantOK := refPublicMemoryFromCanonical(mem)
		if gotOK != wantOK || got != want {
			t.Fatalf("case %d: %v %#v, want %v %#v", c, gotOK, got, wantOK, want)
		}
		text, aliases := memorySearchTextOf(mem)
		if search := memorySearchTextFromMemory(mem); text != search.Text || aliases != search.AliasCount {
			t.Fatalf("case %d: search text %q/%d, want %q/%d", c, text, aliases, search.Text, search.AliasCount)
		}
	}
}
