package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestPresentationViewModelsAreComposedFromTimelinePagesWithoutARoundTrip(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for presentation compose fixture")
		}
	}
	src := readArchiveCenterJS(t)
	if strings.Contains(src, "/presentation/view-model") {
		t.Fatal("the plugin still sends loaded data back to be laid out")
	}
	loadTimeline := extractArchiveCenterJSAsyncFunction(t, src, "loadTimelineData")
	if !strings.Contains(loadTimeline, `params.set("view", "1");`) {
		t.Fatal("the timeline page is not requested with its layout")
	}
	functions := strings.Join([]string{
		extractArchiveCenterJSFunction(t, src, "composePresentationViewModels"),
		extractArchiveCenterJSFunction(t, src, "presentationCompactText"),
		extractArchiveCenterJSFunction(t, src, "timelinePendingItemView"),
		extractArchiveCenterJSFunction(t, src, "composeTimelineViewModel"),
		extractArchiveCenterJSFunction(t, src, "composeSessionRows"),
		extractArchiveCenterJSFunction(t, src, "composeExplorerViewModel"),
		extractArchiveCenterJSFunction(t, src, "getExplorerTabItems"),
	}, "\n")
	script := `
function assert(condition, message) { if (!condition) throw new Error(message); }
function presentationExplorerTabLabel(key) { return key; }
function resolveRuntimeSessionLifecycle(sid) { return sid === "s-gone" ? {status: "deleted", label: "gone", deleted: true} : {status: "inactive", label: "", deleted: false}; }
const memory = {type: "memory", id: 2, turn_index: 5, view: {key: "memory:2", display_turn_key: "turn:5", turn_text: "5", preview: "kept"}};
const reply = {type: "chat_log", id: 1, turn_index: 5, view: {key: "chat_log:1", display_turn_key: "turn:5", turn_text: "5", preview: "reply"}};
const older = {type: "chat_log", id: 3, turn_index: 4, view: {key: "chat_log:3", display_turn_key: "turn:4", turn_text: "4", preview: "older"}};
const _timelineState = {
  selectedSessionId: "s-a", sessionId: "s-a", currentSessionId: "s-b",
  sessions: [{chat_session_id: "s-a", chat_logs_count: 3}, {chat_session_id: "s-b"}, {chat_session_id: "s-gone"}],
  items: [memory, reply, older],
  groups: [
    {key: "turn:5", turn_text: "5", kind: "turn", preview: "kept", counts: {memory: 1, chat_log: 1}, item_keys: ["memory:2", "chat_log:1"]},
    {key: "turn:4", turn_text: "4", kind: "turn", preview: "older", counts: {chat_log: 1}, item_keys: ["chat_log:3", "chat_log:99"]},
    {key: "turn:3", turn_text: "3", kind: "turn", preview: "stale", counts: {chat_log: 1}, item_keys: ["chat_log:7"]},
  ],
  pendingItems: [
    {type: "pending_artifacts", id: "p6", turn_index: 6, chat_session_id: "s-a", title: "Turn #6 saved", preview: "new  **turn**"},
    {type: "pending_artifacts", id: "p5", turn_index: 5, chat_session_id: "s-a", title: "Turn #5 saved"},
    {type: "pending_artifacts", id: "other", turn_index: 9, chat_session_id: "s-other", title: "elsewhere"},
  ],
  meta: {total_unpaged: 40, source_counts: {chat_logs: 2}},
  currentTurn: 5, worldlineTopology: {contract_version: "worldline_topology.viewmodel.v2"},
  loading: false, loadingMore: false, hasMore: true, error: "", viewModel: null,
};
const _explorer = {
  selectedSessionId: "s-a", activeChatSessionId: "s-b", activeTab: "lorebook", viewModel: null,
  sessions: [{chat_session_id: "s-a"}, {chat_session_id: "s-b"}],
  chatLogs: {total: 3}, memories: {total: 2}, directEvidence: {total: 0}, kgTriples: {total: 1},
  episodes: {total: 1}, chapters: {total: 1}, arcs: {total: 1}, sagas: {total: 0},
  trust: {storylines: [1, 2], worldRules: [1], hooks: [1]},
  worldGraph: {rules: [1], allRules: [1, 2, 3, 4, 5]},
  entities: {characters: [1, 2], locations: [1], items: []},
};
` + functions + `
composePresentationViewModels();
const vm = _timelineState.viewModel;
assert(vm.groups.map(g => g.key).join(",") === "turn:6,turn:5,turn:4", "groups: " + vm.groups.map(g => g.key));
assert(vm.groups[0].preview === "new turn" && vm.groups[0].item_count === 1, "a pending-only turn has its own preview");
const turn5 = vm.groups[1];
assert(turn5.items.map(i => i.view.key).join(",") === "pending_artifacts:p5,memory:2,chat_log:1", "turn 5 items: " + turn5.items.map(i => i.view.key));
assert(turn5.preview === "kept" && turn5.counts.pending_artifacts === 1 && turn5.counts.memory === 1, "turn 5 keeps the server preview and adds the pending count");
assert(vm.groups[2].item_count === 1, "a key missing from the page is skipped");
assert(vm.summary.turns === 3 && vm.summary.items === 5 && vm.summary.total === 40, "summary: " + JSON.stringify(vm.summary));
assert(vm.load_more_state === "available" && vm.empty_state === "ready" && vm.current_turn === 5, "states");
assert(vm.worldline.state === "not_applicable" && vm.worldline_topology.contract_version === "worldline_topology.viewmodel.v2", "worldline");
const rows = Object.fromEntries(vm.sessions.map(r => [r.session_id, r]));
assert(rows["s-a"].selected && rows["s-a"].can_copy && rows["s-a"].counts.chat_logs === 3, "selected row");
assert(rows["s-b"].current && !rows["s-b"].can_copy, "the open chat cannot be copied into itself");
assert(rows["s-gone"].deleted && !rows["s-gone"].can_migrate && rows["s-gone"].label === "gone", "a deleted chat cannot be moved");
const ex = _explorer.viewModel;
const counts = Object.fromEntries(ex.tabs.map(tab => [tab.key, tab.count]));
assert(counts.episodes === 3 && counts.trust === 4 && counts.world === 5 && counts.entities === 3, "tab counts: " + JSON.stringify(counts));
assert(ex.sync_state === "different" && _explorer.activeTab === "chat_logs", "a stale lorebook tab falls back to chat logs");

_timelineState.items = []; _timelineState.groups = []; _timelineState.pendingItems = []; _timelineState.loading = true;
composePresentationViewModels();
assert(_timelineState.viewModel.empty_state === "loading" && _timelineState.viewModel.load_more_state === "none", "loading state");
process.stdout.write("ok");
`
	cmd := exec.Command(nodePath, "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("presentation compose fixture failed: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("presentation compose fixture output=%q, want ok", out)
	}
}
