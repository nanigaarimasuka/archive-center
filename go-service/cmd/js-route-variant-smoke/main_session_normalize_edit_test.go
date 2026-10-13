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
		"replay_from_turn: replayFromTurn,",
		`if (choice === "replay")`,
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

func TestSessionNormalizeListsSharedRecordsWithReplay(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for session normalize review fixture")
		}
	}
	src := readArchiveCenterJS(t)
	script := `
function assert(condition, message) { if (!condition) throw new Error(message); }
function escapeAttr(value) { return String(value == null ? "" : value).replace(/"/g, "&quot;"); }
const labels = {
  "sessionNormalize.review.title": "review {count}",
  "sessionNormalize.review.kind.pending_thread": "thread",
  "sessionNormalize.review.changedLater": "from {turn}, changed by {other}",
  "sessionNormalize.review.lastChangedHere": "from {other}, last changed by {turn}",
  "sessionNormalize.review.replay": "replay from {from}",
  "sessionNormalize.replayed": "replayed {count} from {from}",
  "sessionNormalize.replayStopped": "stopped at {turn}, {count} left",
  "sessionNormalize.review.resume": "resume from {from}",
  "sessionNormalize.review.resumeNormalize": "normalize again from {from}",
  "sessionNormalize.retried": "retried ok: {turns}",
  "sessionNormalize.recovered": "recovered: {turns}",
};
function t(key) { return labels[key] || key; }
function tf(key, vars) { let text = t(key); Object.keys(vars || {}).forEach(name => { text = text.split("{" + name + "}").join(String(vars[name])); }); return text; }
` + extractArchiveCenterJSFunction(t, src, "renderSessionNormalizeReviewHtml") + `
const listed = renderSessionNormalizeReviewHtml({
  chat_session_id: "sess-1",
  turn_edits: {
    review_items: [
      {kind: "pending_thread", label: "lunch", turn_index: 37, other_turn: 45, reason: "changed_later"},
      {kind: "pending_thread", label: "duel", turn_index: 37, other_turn: 20, reason: "last_changed_here"},
    ],
    replay_candidate: {from_turn: 37, last_turn: 240, turns: 204},
  },
});
assert(listed.includes("review 2"), "count: " + listed);
assert(listed.includes("from 37, changed by 45") && listed.includes("from 20, last changed by 37"), "reasons: " + listed);
assert(listed.includes('data-session-normalize-replay="37"') && listed.includes('data-session-normalize-replay-last="240"') &&
  listed.includes('data-session-normalize-replay-count="204"') && listed.includes('data-session-normalize-replay-sid="sess-1"'), "replay button: " + listed);
assert(renderSessionNormalizeReviewHtml({turn_edits: {review_items: []}}) === "", "nothing to review renders nothing");
const replayed = renderSessionNormalizeReviewHtml({replay: {status: "ok", from_turn: 37, turns: 204}, turn_edits: {review_items: [{kind: "status"}]}});
assert(replayed.includes("replayed 204 from 37") && !replayed.includes("data-session-normalize-replay="), "after a replay: " + replayed);
const stopped = renderSessionNormalizeReviewHtml({
  chat_session_id: "sess-1",
  rescan: {recovered_turns: [181], stopped_at_turn: 52, remaining_turns: 7,
    retried_turns: [{turn_index: 40, retries: 1, state: "completed"}, {turn_index: 52, retries: 2, state: "retryable"}]},
  replay: {status: "stopped", stopped_at_turn: 52, remaining_turns: 189, last_turn: 240},
});
assert(stopped.includes("stopped at 52, 189 left") && stopped.includes("resume from 52"), "stopped replay: " + stopped);
assert(stopped.includes('data-session-normalize-replay="52"') && stopped.includes('data-session-normalize-replay-count="189"'), "resume button: " + stopped);
assert(stopped.includes("retried ok: 40") && !stopped.includes("retried ok: 40, 52"), "only turns that succeeded on retry: " + stopped);
assert(stopped.includes("recovered: 181"), "worker-recovered turns: " + stopped);
const plainStop = renderSessionNormalizeReviewHtml({
  chat_session_id: "sess-1",
  rescan: {stopped_at_turn: 181, remaining_turns: 59},
  replay: {status: "skipped"},
});
assert(plainStop.includes("stopped at 181, 59 left") && plainStop.includes('data-session-normalize-resume="sess-1"') &&
  plainStop.includes("normalize again from 181") && !plainStop.includes("data-session-normalize-replay="), "stopped normalize resumes by running again: " + plainStop);
process.stdout.write("ok");
`
	cmd := exec.Command(nodePath, "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("session normalize review fixture failed: %v\n%s", err, out)
	}
}

func TestRestartFromTurnDeletesThenNormalizesTheOpenChat(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for the restart fixture")
		}
	}
	src := readArchiveCenterJS(t)
	script := `
function assert(condition, message) { if (!condition) throw new Error(message); }
const calls = [];
const alerts = [];
let confirmAnswer = true;
let hostContext = {hostChatId: "chat-1"};
let rollbackOK = true;
const _sessionNormalizeState = {loading: false};
function t(key) { return key; }
function tf(key, vars) { return key + " " + JSON.stringify(vars); }
function alert(text) { alerts.push(text); }
function captureSessionHostContextFromCache() { return hostContext; }
async function computeActiveChatRescanDryRunPlan() { return {ok: true, pairs: [3, 4, 5, 6].map(n => ({turnIndex: n}))}; }
async function showConfirmModal(title, text) { calls.push("confirm " + text); return confirmAnswer; }
async function executeAutoRollback(sid, turn, reason, detail, options) { calls.push("rollback " + sid + " " + turn + " " + options.requestSource); return rollbackOK; }
async function normalizeSession(sid, max, options) { calls.push("normalize " + sid + " " + options.skipConfirm + " " + options.source); return true; }
` + extractJSFunctionBlockForTest(t, src, "async function restartSessionFromTurn(sessionId, turnValue)") + `
(async () => {
  assert(await restartSessionFromTurn("sess", "4") === true, "restart ran");
  assert(calls.length === 3 && calls[0].includes('"from":4') && calls[0].includes('"last":6') && calls[0].includes('"count":3'), "confirm first: " + calls[0]);
  assert(calls[1] === "rollback sess 4 manual" && calls[2] === "normalize sess true explorer_session_normalize_restart", "rollback then normalize: " + calls);

  calls.length = 0;
  for (const bad of ["", "0", "2.5", "-3"]) {
    assert(await restartSessionFromTurn("sess", bad) === false, "bad turn " + bad);
  }
  assert(await restartSessionFromTurn("sess", "9") === false && alerts.at(-1).includes("missingTurn"), "turn past the Risu chat");
  hostContext = null;
  assert(await restartSessionFromTurn("sess", "4") === false && alerts.at(-1) === "sessionNormalize.restart.notActive", "not the open chat");
  hostContext = {hostChatId: "chat-1"};
  confirmAnswer = false;
  assert(await restartSessionFromTurn("sess", "4") === false, "cancelled");
  confirmAnswer = true;
  rollbackOK = false;
  assert(await restartSessionFromTurn("sess", "4") === false && alerts.at(-1).includes("restart.failed"), "rollback failed");
  assert(!calls.some(c => c.startsWith("normalize")) && calls.filter(c => c.startsWith("rollback")).length === 1, "nothing rebuilt without a rollback: " + calls);
  process.stdout.write("ok");
})().catch(err => { console.error(err.message); process.exit(1); });
`
	cmd := exec.Command(nodePath, "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("restart fixture failed: %v\n%s", err, out)
	}
}

func TestNormalizeShowsSummaryProgressAndWaitingApartFromBlocks(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for the summary progress fixture")
		}
	}
	src := readArchiveCenterJS(t)
	functions := strings.Join([]string{
		extractArchiveCenterJSFunction(t, src, "hierarchyOpenTail"),
		extractArchiveCenterJSFunction(t, src, "hierarchyBlockedOnly"),
		extractArchiveCenterJSFunction(t, src, "formatHierarchyBlockedPreview"),
		extractArchiveCenterJSFunction(t, src, "formatHierarchyBlockedSummary"),
		extractArchiveCenterJSFunction(t, src, "formatHierarchyWaitingSummary"),
		extractArchiveCenterJSFunction(t, src, "renderSessionNormalizeTurnsHtml"),
	}, "\n")
	script := `
function assert(condition, message) { if (!condition) throw new Error(message); }
function escapeAttr(value) { return String(value == null ? "" : value); }
function formatTurnIndexPreview(turns) { return turns.join(","); }
const labels = {
  "explorer.hierarchy.layer.chapter": "chapter", "explorer.hierarchy.layer.arc": "arc", "explorer.hierarchy.layer.saga": "saga",
  "sessionNormalize.count.episodes": "episodes", "sessionNormalize.count.arcs": "arcs",
  "sessionNormalize.hierarchyWaiting": "waiting: {items}",
  "sessionNormalize.hierarchyWaitingItem": "{layer} {from}-{to} {child} {have}/{need}",
  "sessionNormalize.currentSummary": "making {layer} {from}-{to} #{count}",
};
function t(key) { return labels[key] || key; }
function tf(key, vars) { let text = t(key); Object.keys(vars || {}).forEach(name => { text = text.split("{" + name + "}").join(String(vars[name])); }); return text; }
` + functions + `
const hb = {
  chapter: {blocked: [
    {from_turn: 241, to_turn: 255, reason: "open_tail_episode_count", have: 3, need: 12},
    {from_turn: 61, to_turn: 120, reason: "blocked_missing_episode"},
  ]},
  saga: {blocked: [{from_turn: 1, to_turn: 240, reason: "open_tail_arc_count", have: 1, need: 4}]},
};
const blocked = formatHierarchyBlockedSummary(hb);
assert(blocked === "chapter blocked 1 (#61-120:blocked_missing_episode)", "only real blocks count as blocked: " + blocked);
const waiting = formatHierarchyWaitingSummary(hb);
assert(waiting === "waiting: chapter 241-255 episodes 3/12 / saga 1-240 arcs 1/4", "open tails are waiting: " + waiting);
assert(formatHierarchyBlockedSummary({saga: {blocked: [{from_turn: 1, to_turn: 240, reason: "open_tail_arc_count"}]}}) === "", "a lone open tail is not blocked");
assert(formatHierarchyWaitingSummary({}) === "", "nothing waiting");

const progress = renderSessionNormalizeTurnsHtml({current_summary: {layer: "chapter", from_turn: 181, to_turn: 240, count: 2}}, {terminal: false});
assert(progress.includes("making chapter 181-240 #2"), "summary being made: " + progress);
assert(renderSessionNormalizeTurnsHtml({current_summary: null}, {terminal: false}) === "", "no summary, no line");
process.stdout.write("ok");
`
	cmd := exec.Command(nodePath, "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("summary progress fixture failed: %v\n%s", err, out)
	}
}
