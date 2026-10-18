package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestFindReplaceTextOnlySendsEditedTurnsAndKeepsMemory(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for the find and replace fixture")
		}
	}
	src := readArchiveCenterJS(t)
	if !strings.Contains(src, `if (choice === "text") return reflectFindReplaceTextOnly(sid, report);`) {
		t.Fatal("the reflect choice does not offer the text-only path")
	}
	script := `
function assert(condition, message) { if (!condition) throw new Error(message); }
const t = key => key, tf = (key, values) => key + " " + JSON.stringify(values || {});
const formatTurnIndexPreview = turns => turns.join(",");
function normalizeTurnIndexList(list) { return list.slice().sort((a, b) => a - b); }
let hostContext = {chat: 1}, cleared = 0, sent = null, response = null;
const captureSessionHostContextFromCache = () => hostContext;
const plan = {ok: true,
  rows: [
    {turn_index: 2, raw_status: "match", user_present: true, assistant_present: true},
    {turn_index: 3, raw_status: "mismatch_or_partial", user_present: true, assistant_present: true},
    {turn_index: 4, raw_status: "mismatch_or_partial", user_present: true, assistant_present: true}],
  pairs: [
    {turnIndex: 2, userContent: "a", assistantContent: "b"},
    {turnIndex: 3, userContent: "Mina waves", assistantContent: "Rook nods"},
    {turnIndex: 4, userContent: "Mina sits", assistantContent: "Rook leaves"}]};
const computeActiveChatRescanDryRunPlan = async () => plan;
async function clearFindReplacePending() { cleared++; }
async function safeCall(fn) { return fn(); }
async function bridgeFetch(path, options) { sent = {path, body: options.body}; return response; }
` + extractArchiveCenterJSFunction(t, src, "buildSessionNormalizeEditedTurnsFromDryRunPlan") + "\n" +
		extractArchiveCenterJSAsyncFunction(t, src, "reflectFindReplaceTextOnly") + `
(async () => {
  const reports = [];
  const report = (text, isError) => reports.push([text, isError]);
  response = {status: "ok", updated_turns: [3, 4], failed: [], skipped: []};
  await reflectFindReplaceTextOnly("sid", report);
  assert(sent.path === "/admin/session-turn-text", "path " + sent.path);
  assert(JSON.stringify(sent.body.entries.map(e => e.turn_index)) === "[3,4]", "entries " + JSON.stringify(sent.body.entries));
  assert(sent.body.entries[0].user_content === "Mina waves", "text sent as it is now");
  assert(cleared === 1 && reports[0][0].startsWith("findReplace.textOnlyDone") && reports[0][1] === false, "done " + JSON.stringify(reports));

  response = {status: "partial_error", updated_turns: [3], failed: [{turn_index: 4, reason: "turn_edit_message_missing"}], skipped: []};
  await reflectFindReplaceTextOnly("sid", report);
  assert(cleared === 1 && reports[1][1] === true, "a failed turn keeps the changes waiting");

  hostContext = null; sent = null;
  await reflectFindReplaceTextOnly("sid", report);
  assert(sent === null && reports[2][0] === "findReplace.textOnlyNeedsOpenChat", "needs the open chat");
  process.stdout.write("ok");
})().catch(err => { console.error(err.message); process.exit(1); });
`
	cmd := exec.Command(nodePath, "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("find and replace text-only fixture failed: %v\n%s", err, out)
	}
}
