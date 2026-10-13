package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSessionDiagnoseOnlyReadsAndListsWhatNormalizeWouldFix(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for session diagnose fixture")
		}
	}
	src := readArchiveCenterJS(t)
	functions := strings.Join([]string{
		extractArchiveCenterJSFunction(t, src, "normalizeTurnIndexList"),
		extractArchiveCenterJSFunction(t, src, "buildSessionNormalizeEditedTurnsFromDryRunPlan"),
		extractArchiveCenterJSAsyncFunction(t, src, "diagnoseSession"),
		extractArchiveCenterJSFunction(t, src, "renderSessionDiagnoseHtml"),
	}, "\n")
	script := `
function assert(condition, message) { if (!condition) throw new Error(message); }
const settings = {enabled:true, dbEnabled:true, rollbackAutoEnabled:true};
let _sessionDiagnoseState = { sessionId: "", loading: false, error: null, result: null };
const _sessionNormalizeState = { panelOpen: false };
let _summaryFailureState = { sessionId: "", report: null };
const requests = [];
function t(key) { return key; }
function tf(key, vars) { return key + ":" + vars.turn; }
function escapeAttr(value) { return String(value); }
function formatTurnIndexPreview(turns) { return turns.join(","); }
function summaryFailureRows(report) { return report && report.rows ? report.rows : []; }
function refreshExplorerUI() {}
function getRequestTimeoutSettingMs() { return 1000; }
function buildAdminRuntimeClientMeta(meta) { return meta; }
async function safeCall(fn) { return fn(); }
function captureSessionHostContextFromCache(sid) { return sid === "live" ? {hostChatId: "chat"} : null; }
async function resolveCurrentActiveChatObject() { return {chat: {}}; }
async function askActiveChatTailDeletionDecision(sid, chat, options) {
  requests.push("decision:" + options.reason);
  return {decision: {allowed: true, from_turn: 41}};
}
async function executeAutoRollback() { throw new Error("diagnose rolled back"); }
async function computeActiveChatRescanDryRunPlan() {
  return {
    ok: true,
    rawMissingTurns: [7],
    rawMismatchTurns: [10, 12],
    pairs: [{turnIndex: 10, userContent: "u", assistantContent: "a"}, {turnIndex: 12, userContent: "", assistantContent: "a"}],
    rows: [
      {turn_index: 10, raw_status: "mismatch_or_partial", user_present: true, assistant_present: true},
      {turn_index: 12, raw_status: "mismatch_or_partial", user_present: true, assistant_present: true},
    ],
  };
}
async function loadSummaryFailures(sid) { _summaryFailureState = {sessionId: sid, report: {rows: []}}; }
async function bridgeFetch(path, options) {
  requests.push(path);
  assert(path === "/admin/reindex", "diagnose called " + path);
  assert(options.body.dry_run === true && options.body.background === false, "the vector check was not a dry run");
  return {status: "ok", vector_gaps: {missing_count: 3, orphan_count: 2}, pre_reindex_integrity: {reembed_reasons: []}};
}
` + functions + `
(async function() {
  assert(await diagnoseSession("live") === true, "diagnose failed");
  assert(JSON.stringify(requests) === JSON.stringify(["decision:session_diagnose_full_active_chat_observed", "/admin/reindex"]), "requests: " + JSON.stringify(requests));
  const r = _sessionDiagnoseState.result;
  assert(r.rollbackFromTurn === 41, "rollback turn");
  assert(JSON.stringify(r.missingTurns) === "[7]", "missing turns");
  assert(JSON.stringify(r.editedTurns) === "[10]", "edited turns");
  assert(JSON.stringify(r.reviewTurns) === "[12]", "an edited turn was also listed for review");
  const html = renderSessionDiagnoseHtml(_sessionDiagnoseState, "live");
  ["sessionDiagnose.rollback:41", "sessionDiagnose.missing (1): 7", "sessionDiagnose.edited (1): 10", "sessionDiagnose.review (1): 12",
   "sessionDiagnose.vectorMissing: 3", "sessionDiagnose.vectorOrphans: 2", "sessionDiagnose.hint"].forEach(function(text) {
    assert(html.includes(text), "missing " + text + " in " + html);
  });

  requests.length = 0;
  bridgeFetch = async function() { requests.push("/admin/reindex"); return {status: "ok", vector_gaps: {}, pre_reindex_integrity: {}}; };
  assert(await diagnoseSession("other") === true, "diagnose of another session failed");
  assert(JSON.stringify(requests) === JSON.stringify(["/admin/reindex"]), "a closed chat was compared with Risu: " + JSON.stringify(requests));
  const clean = renderSessionDiagnoseHtml(_sessionDiagnoseState, "other");
  assert(clean.includes("sessionDiagnose.clean") && clean.includes("sessionDiagnose.notLive"), "clean result: " + clean);
  process.stdout.write("ok");
})().catch(function(err) {
  console.error(err && err.stack || err);
  process.exit(1);
});
`
	cmd := exec.Command(nodePath, "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("session diagnose fixture failed: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("session diagnose fixture output=%q, want ok", out)
	}
}
