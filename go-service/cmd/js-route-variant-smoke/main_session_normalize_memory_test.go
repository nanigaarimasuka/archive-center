package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSessionNormalizeFindsMissingMemoryOnEpisodeEndTurns(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for session normalize memory fixture")
		}
	}
	src := readArchiveCenterJS(t)
	functions := strings.Join([]string{
		extractArchiveCenterJSFunction(t, src, "buildActiveChatRescanDerivedMap"),
		extractArchiveCenterJSFunction(t, src, "buildActiveChatRescanDryRunRows"),
	}, "\n")
	script := `
function normalizeActiveChatRescanCompareText(value) { return String(value || "").trim(); }
function assert(condition, message) { if (!condition) throw new Error(message); }
` + functions + `
const pairs = [394, 395, 396].map(function(turnIndex) {
  return {turnIndex, userContent: "u" + turnIndex, assistantContent: "a" + turnIndex, inputMode: "paired"};
});
const raw = new Map(pairs.map(function(pair) {
  return [pair.turnIndex, {userPresent: true, assistantPresent: true, user: pair.userContent, assistant: pair.assistantContent}];
}));
const derived = buildActiveChatRescanDerivedMap([
  {type: "memory", turn_index: 394},
  {type: "episode", turn_index: 395, from_turn: 391, to_turn: 395},
  {type: "kg_triple", turn_index: 396},
]);
const status = {};
buildActiveChatRescanDryRunRows(pairs, raw, derived).forEach(function(row) { status[row.turn_index] = row.derived_status; });
assert(status[394] === "present", "a turn with its memory was flagged");
assert(status[395] === "missing_suspected", "the episode ending on a turn hid its missing memory");
assert(status[396] === "missing_suspected", "relations hid a missing memory");
process.stdout.write("ok");
`
	cmd := exec.Command(nodePath, "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("session normalize memory JS fixture failed: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("session normalize memory JS fixture output=%q, want ok", out)
	}
}

func TestEachTurnRestartsAStoppedRisuModelBridge(t *testing.T) {
	src := readArchiveCenterJS(t)
	beforeRequest := extractJSFunctionBlockForTest(t, src, "async function onBeforeRequest(payload, type)")
	enabledGate := strings.Index(beforeRequest, `if (!settings.enabled || !isSaveType(type)) return payload;`)
	restart := strings.Index(beforeRequest, `startRisuModelBridge();`)
	if enabledGate < 0 || restart < enabledGate {
		t.Fatal("a turn does not restart the Risu model bridge after the enabled check")
	}
	bridge := extractJSFunctionBlockForTest(t, src, "function startRisuModelBridge()")
	if !strings.Contains(bridge, `if (_risuBridgeRunning ||`) {
		t.Fatal("restarting a running Risu model bridge would start a second poll loop")
	}
}
