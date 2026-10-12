package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSummaryFailureCardListsGapsAndOffersNormalize(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for summary failure card fixture")
		}
	}
	src := readArchiveCenterJS(t)
	functions := strings.Join([]string{
		extractArchiveCenterJSFunction(t, src, "formatTurnIndexPreview"),
		extractArchiveCenterJSFunction(t, src, "formatSummaryFailureRanges"),
		extractArchiveCenterJSFunction(t, src, "summaryFailureRows"),
		extractArchiveCenterJSFunction(t, src, "renderSummaryFailureCard"),
		extractArchiveCenterJSFunction(t, src, "renderSummaryFailureListHtml"),
	}, "\n")
	script := `
function assert(condition, message) { if (!condition) throw new Error(message); }
function t(key) { return key; }
function escapeAttr(value) { return String(value == null ? "" : value).replace(/"/g, "&quot;").replace(/</g, "&lt;"); }
function normalizeTurnIndexList(list) { return (Array.isArray(list) ? list : []).map(Number).filter(function(n) { return n > 0; }); }
const _sessionNormalizeState = {loading: false};
` + functions + `
const failed = {
  sessionId: "sess-a", loading: false, error: null, failureCount: 3,
  report: {
    missing_memory_turns: [395],
    episodes: [{from_turn: 391, to_turn: 395, reason: "blocked_missing_memory", missing_memory_turns: [395]}],
    chapters: [{from_turn: 1, to_turn: 60, reason: "chapter_missing"}],
    arcs: [], sagas: [],
  },
};
const card = renderSummaryFailureCard(failed);
assert(card.includes("mo-dash-card has-fail"), "failures must use the failing dashboard card style");
assert(card.includes("391–395") && card.includes("1–60"), "card must list the failed ranges");
assert(card.includes('id="mo-summary-failure-normalize-btn"') && card.includes('data-session-id="sess-a"'), "card must offer session normalize for the session");
assert(!card.includes("summaryFailures.arcs"), "empty layers must not be listed");

const clean = renderSummaryFailureCard({sessionId: "sess-a", report: {missing_memory_turns: [], episodes: [], chapters: [], arcs: [], sagas: []}});
assert(clean.includes("summaryFailures.none") && !clean.includes("mo-summary-failure-normalize-btn"), "a clean session shows no button");
assert(renderSummaryFailureCard({sessionId: "sess-a", loading: true}) === "", "a loading check renders nothing");

assert(renderSummaryFailureListHtml(failed, "sess-a").includes("391–395"), "normalize panel lists the failures");
assert(renderSummaryFailureListHtml(failed, "sess-b") === "", "another session's failures are not shown");
process.stdout.write("ok");
`
	cmd := exec.Command(nodePath, "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("summary failure card fixture failed: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("summary failure card fixture output=%q, want ok", out)
	}
}

func TestHierarchyTabShowsFailedRanges(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for hierarchy failure rows fixture")
		}
	}
	src := readArchiveCenterJS(t)
	if !strings.Contains(extractJSFunctionBlockForTest(t, src, "function renderExplorerHierarchy()"), "renderHierarchyFailureRows(subTab, _summaryFailureState, _explorer.selectedSessionId)") {
		t.Fatal("the hierarchy tab does not show failed ranges")
	}
	functions := strings.Join([]string{
		extractArchiveCenterJSFunction(t, src, "formatTurnIndexPreview"),
		extractArchiveCenterJSFunction(t, src, "renderHierarchyFailureRows"),
	}, "\n")
	script := `
function assert(condition, message) { if (!condition) throw new Error(message); }
function t(key) { return key; }
function tf(key, vars) { return key + ":" + JSON.stringify(vars); }
function escapeAttr(value) { return String(value == null ? "" : value); }
function normalizeTurnIndexList(list) { return (Array.isArray(list) ? list : []).map(Number).filter(function(n) { return n > 0; }); }
` + functions + `
const state = {sessionId: "s", report: {
  episodes: [{from_turn: 391, to_turn: 395, missing_memory_turns: [395]}],
  chapters: [{from_turn: 1, to_turn: 60}],
}};
const episodes = renderHierarchyFailureRows("episodes", state, "s");
assert(episodes.includes("391-395") && episodes.includes("summaryFailures.rowHeld") && episodes.includes("395"), "held episode row: " + episodes);
const chapters = renderHierarchyFailureRows("chapters", state, "s");
assert(chapters.includes("1-60") && chapters.includes("summaryFailures.rowFailed"), "failed chapter row");
assert(renderHierarchyFailureRows("arcs", state, "s") === "", "no rows for a clean layer");
assert(renderHierarchyFailureRows("episodes", state, "other") === "", "another session's failures stay hidden");
process.stdout.write("ok");
`
	cmd := exec.Command(nodePath, "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hierarchy failure rows fixture failed: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("hierarchy failure rows fixture output=%q, want ok", out)
	}
}

func TestNormalizeOffersToRebuildFlaggedSummaries(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for hierarchy rebuild fixture")
		}
	}
	src := readArchiveCenterJS(t)
	normalize := extractJSFunctionBlockForTest(t, src, "async function normalizeSession(sessionId, maxItems, options = {})")
	for _, want := range []string{"rebuildHierarchy = await showConfirmModal(", "rebuild_hierarchy: rebuildHierarchy,"} {
		if !strings.Contains(normalize, want) {
			t.Fatalf("normalizeSession is missing %q", want)
		}
	}
	list := extractJSFunctionBlockForTest(t, src, "function renderExplorerHierarchyList(kind)")
	if !strings.Contains(list, `class="mo-hier-regen-btn"`) {
		t.Fatal("chapter, arc, and saga rows have no rebuild button")
	}
	functions := strings.Join([]string{
		extractArchiveCenterJSFunction(t, src, "formatTurnIndexPreview"),
		extractArchiveCenterJSFunction(t, src, "formatSummaryFailureRanges"),
		extractArchiveCenterJSFunction(t, src, "formatHierarchyRebuildTargets"),
		extractArchiveCenterJSFunction(t, src, "summaryFailureRows"),
	}, "\n")
	script := `
function assert(condition, message) { if (!condition) throw new Error(message); }
function t(key) { return key; }
function normalizeTurnIndexList(list) { return (Array.isArray(list) ? list : []).map(Number).filter(function(n) { return n > 0; }); }
` + functions + `
const rebuild = [{layer: "chapter", from_turn: 1, to_turn: 60, reasons: ["broken_list_fields"]}, {layer: "arc", from_turn: 1, to_turn: 240, reasons: ["contains_rebuilt_summary"]}];
const text = formatHierarchyRebuildTargets(rebuild);
assert(text.includes("explorer.hierarchy.layer.chapter 1-60 (summaryFailures.rebuildReason.broken_list_fields)"), text);
assert(text.includes("explorer.hierarchy.layer.arc 1-240"), text);
const rows = summaryFailureRows({missing_memory_turns: [], episodes: [], chapters: [], arcs: [], sagas: [], rebuild});
assert(rows.length === 1 && rows[0][0] === "summaryFailures.rebuild" && rows[0][2] === 2, JSON.stringify(rows));
process.stdout.write("ok");
`
	cmd := exec.Command(nodePath, "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hierarchy rebuild fixture failed: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("hierarchy rebuild fixture output=%q, want ok", out)
	}
}
