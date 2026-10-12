package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSessionNormalizeTakesEditedTurnsOnly(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for session normalize edit fixture")
		}
	}
	src := readArchiveCenterJS(t)
	normalize := extractJSFunctionBlockForTest(t, src, "async function normalizeSession(sessionId, maxItems, options = {})")
	for _, want := range []string{
		"applyTurnEdits = buildSessionNormalizeEditedTurnsFromDryRunPlan(plan);",
		"applyTurnEdits.length >= SESSION_NORMALIZE_EDIT_CONFIRM_TURNS",
		"apply_turn_edits: applyTurnEdits,",
	} {
		if !strings.Contains(normalize, want) {
			t.Fatalf("normalizeSession is missing %q", want)
		}
	}
	functions := strings.Join([]string{
		extractArchiveCenterJSFunction(t, src, "normalizeTurnIndexList"),
		extractArchiveCenterJSFunction(t, src, "buildSessionNormalizeEditedTurnsFromDryRunPlan"),
	}, "\n")
	script := `
function assert(condition, message) { if (!condition) throw new Error(message); }
` + functions + `
const plan = {
  pairs: [
    {turnIndex: 10, userContent: "edited user", assistantContent: "edited reply"},
    {turnIndex: 11, userContent: "u", assistantContent: "a"},
    {turnIndex: 12, userContent: "", assistantContent: "only reply"},
    {turnIndex: 13, userContent: "u", assistantContent: "a"},
  ],
  rows: [
    {turn_index: 10, raw_status: "mismatch_or_partial", user_present: true, assistant_present: true},
    {turn_index: 11, raw_status: "mismatch_or_partial", user_present: false, assistant_present: true},
    {turn_index: 12, raw_status: "mismatch_or_partial", user_present: true, assistant_present: true},
    {turn_index: 13, raw_status: "present", user_present: true, assistant_present: true},
  ],
};
const edited = buildSessionNormalizeEditedTurnsFromDryRunPlan(plan);
assert(JSON.stringify(edited) === "[10]", "only a fully stored turn with new text in both messages is an edit: " + JSON.stringify(edited));
process.stdout.write("ok");
`
	cmd := exec.Command(nodePath, "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("session normalize edit fixture failed: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("session normalize edit fixture output=%q, want ok", out)
	}
}
