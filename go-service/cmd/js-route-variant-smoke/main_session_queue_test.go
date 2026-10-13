package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSessionQueuePanelStreamsCountsAndCancelsAll(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for session queue fixture")
		}
	}
	src := readArchiveCenterJS(t)
	functions := strings.Join([]string{
		extractArchiveCenterJSFunction(t, src, "renderSessionQueueLaneHtml"),
		extractArchiveCenterJSFunction(t, src, "renderSessionQueueInnerHtml"),
		extractArchiveCenterJSFunction(t, src, "renderSessionQueueHtml"),
		extractArchiveCenterJSFunction(t, src, "sessionQueuePanel"),
		extractArchiveCenterJSFunction(t, src, "paintSessionQueuePanel"),
		extractArchiveCenterJSFunction(t, src, "stopSessionQueueStream"),
		extractArchiveCenterJSFunction(t, src, "ensureSessionQueueStream"),
		extractArchiveCenterJSAsyncFunction(t, src, "cancelSessionQueues"),
	}, "\n")
	script := `
function assert(condition, message) { if (!condition) throw new Error(message); }
let _sessionQueueState = { sessionId: "", counts: null, disconnected: false, cancelling: false, notice: "", stream: null };
const settings = { bridgeUrl: "http://bridge" };
const requests = [];
function t(key) { return key; }
function tf(key, vars) { return key + ":" + JSON.stringify(vars); }
function escapeAttr(value) { return String(value); }
function resolveBridgeRuntimeRoute(url) { return { url }; }
function getRequestTimeoutSettingMs() { return 1000; }
async function safeCall(fn) { return fn(); }
async function showConfirmModal() { return true; }
async function bridgeFetch(path, init) {
  requests.push(init.method + " " + path + " " + JSON.stringify(init.body));
  return { status: "ok", vector_withdrawn: 7, reprocessing_withdrawn: 2 };
}
const panel = { attrs: { "data-session-queue-panel": "s1" }, innerHTML: "", getAttribute(name) { return this.attrs[name]; } };
let panelShown = true;
const document = { querySelector() { return panelShown ? panel : null; } };
const encoder = new TextEncoder();
let pushChunk = null;
let streamUrl = "";
async function openTurnWorkflowHUDStream(url) {
  streamUrl = url;
  return {
    read() { return new Promise(function(resolve) { pushChunk = resolve; }); },
    cancel() { return Promise.resolve(); },
  };
}
` + functions + `
(async function() {
  assert(renderSessionQueueHtml("s1").includes("sessionQueue.loading"), "counts show as loading before the first event");
  ensureSessionQueueStream("s1");
  await new Promise(function(resolve) { setImmediate(resolve); });
  assert(streamUrl === "http://bridge/admin/queues/events?chat_session_id=s1", "streams the selected session: " + streamUrl);
  const first = _sessionQueueState.stream;
  ensureSessionQueueStream("s1");
  assert(_sessionQueueState.stream === first, "one stream per session");
  pushChunk({ value: encoder.encode(JSON.stringify({ status: "ok", total: 3,
    vector: { waiting: 2, running: 1, retrying: 0, total: 3 },
    reprocessing: { waiting: 0, running: 0, retrying: 0, total: 0 } }) + "\n"), done: false });
  await new Promise(function(resolve) { setImmediate(resolve); });
  assert(panel.innerHTML.includes("sessionQueue.vector") && panel.innerHTML.includes("\"waiting\":\"2\""), "paints the vector counts: " + panel.innerHTML);
  assert(panel.innerHTML.includes("sessionQueue.reprocessing</strong> sessionQueue.empty"), "an empty lane reads as empty");
  assert(panel.innerHTML.includes("data-session-queue-cancel"), "offers cancel while work is queued");

  await cancelSessionQueues("s1");
  assert(requests.length === 1 && requests[0] === "POST /admin/queues/cancel {\"chat_session_id\":\"s1\"}", "cancels the session's queues: " + requests);
  assert(_sessionQueueState.notice.includes("\"vector\":\"7\""), "reports what was withdrawn: " + _sessionQueueState.notice);

  panelShown = false;
  pushChunk({ value: encoder.encode(JSON.stringify({ status: "ok", total: 0, vector: {}, reprocessing: {} }) + "\n"), done: false });
  await new Promise(function(resolve) { setImmediate(resolve); });
  assert(_sessionQueueState.stream === null, "the stream stops once the panel is gone");
  console.log("ok");
})().catch(function(err) { console.error(err && err.stack || err); process.exit(1); });
`
	cmd := exec.Command(nodePath, "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("session queue fixture failed: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("session queue fixture output=%q, want ok", out)
	}
}

func TestSessionNormalizeShowsItsTurnsAndKeepsDetailsOpen(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for session normalize fixture")
		}
	}
	src := readArchiveCenterJS(t)
	script := `
function assert(condition, message) { if (!condition) throw new Error(message); }
const _keptOpenDetails = new Set();
function tf(key, vars) { return key + ":" + (vars.turn || vars.turns); }
function escapeAttr(value) { return String(value); }
function formatTurnIndexPreview(turns) { return turns.join(","); }
` + extractArchiveCenterJSFunction(t, src, "renderSessionNormalizeTurnsHtml") + "\n" +
		extractArchiveCenterJSFunction(t, src, "renderKeptOpenDetails") + `
assert(renderSessionNormalizeTurnsHtml({ current_turn: 42 }, {}).includes("sessionNormalize.currentTurn:42"), "shows the current turn");
const waiting = renderSessionNormalizeTurnsHtml({ current_turn: null, running_turns: [3], waiting_turns: [4, 5] }, {});
assert(waiting.includes("sessionNormalize.runningTurns:3") && waiting.includes("sessionNormalize.waitingTurns:4,5"), "shows reprocessing turns: " + waiting);
assert(renderSessionNormalizeTurnsHtml({ current_turn: 42 }, { terminal: true }) === "", "a finished job shows no turn");
assert(!renderKeptOpenDetails("k", "c", "s", "b").includes(" open"), "closed by default");
_keptOpenDetails.add("k");
assert(renderKeptOpenDetails("k", "c", "s", "b").includes(" open"), "stays open across renders");
console.log("ok");
`
	cmd := exec.Command(nodePath, "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("session normalize turns fixture failed: %v\n%s", err, out)
	}
}
