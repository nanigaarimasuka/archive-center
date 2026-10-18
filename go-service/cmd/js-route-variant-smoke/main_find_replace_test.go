package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Find and replace: the preview must show exactly what String.prototype.replace
// would make, and applying must touch only the previewed, unchanged messages.
func TestFindReplaceMatchesStringReplaceAndAppliesOnlyPreviewedMessages(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for find and replace fixture")
		}
	}
	src := readArchiveCenterJS(t)
	for _, required := range []string{
		`["find-replace", t('findReplace.tab')]`,
		`opts.skipConfirm === true || await showConfirmModal(`,
		`applyTurnEdits.length >= SESSION_NORMALIZE_EDIT_CONFIRM_TURNS && opts.acceptEdits !== true`,
		`acceptEdits: true,`,
		`if (editsCovered) await clearFindReplacePending(sid);`,
		`editsCovered = applyTurnEdits.length > 0 || !(Array.isArray(plan.rawMismatchTurns) && plan.rawMismatchTurns.length > 0);`,
		`await addFindReplacePending(preview.sessionId, result.changed.length);`,
	} {
		if !strings.Contains(src, required) {
			t.Fatalf("Archive Center.js missing %q", required)
		}
	}
	functions := strings.Join([]string{
		extractArchiveCenterJSFunction(t, src, "buildFindReplaceMatcher"),
		extractArchiveCenterJSFunction(t, src, "expandFindReplaceReplacement"),
		extractArchiveCenterJSFunction(t, src, "findReplaceInText"),
		extractArchiveCenterJSFunction(t, src, "buildFindReplacePreview"),
		extractArchiveCenterJSFunction(t, src, "applyFindReplaceEntries"),
	}, "\n")
	script := functions + `
function assert(condition, message) { if (!condition) throw new Error(message); }
const text = "Kim met Lee. kim? KIM!!!\nLee said: kim-lee 2024-10-18 $5";
const cases = [
  ["kim", false, false, "Park"],
  ["kim", false, true, "$&-$&"],
  ["(\\w+)-(\\w+)", true, false, "$2-$1"],
  ["(?<y>\\d{4})-(?<m>\\d{2})-(?<d>\\d{2})", true, false, "$<d>.$<m>.$<y> ($$) [$1] {$10} <$9>"],
  ["^Lee", true, true, "[$&]"],
  ["e(?=e)", true, false, "E"],
  ["\\b", true, false, "|"],
  ["$5", false, false, "$1$&$'"],
  ["(l)(e)(e)", true, false, "$3$2$1$4$03"],
];
for (const [query, regex, caseSensitive, replacement] of cases) {
  const matcher = buildFindReplaceMatcher(query, { regex, caseSensitive });
  assert(matcher.regex, "matcher failed for " + query);
  const ours = findReplaceInText(text, matcher.regex, replacement, regex).after;
  // Literal mode keeps the replacement as typed, so compare with a function.
  const native = text.replace(new RegExp(matcher.regex.source, matcher.regex.flags), regex ? replacement : function() { return replacement; });
  assert(ours === native, "differs for " + query + " / " + replacement + ":\n" + ours + "\n" + native);
}
assert(buildFindReplaceMatcher("", {}).error === "empty", "empty query accepted");
assert(buildFindReplaceMatcher("a(", { regex: true }).error === "invalid", "invalid regex accepted");
assert(buildFindReplaceMatcher("a(", { regex: false }).regex.test("xa("), "literal query not escaped");

const messages = [
  { role: "user", chatId: "u1", data: "hello world" },
  { role: "char", chatId: "c1", data: "world of worlds" },
  { role: "char", chatId: "c2", data: "nothing here" },
  { role: "user", chatId: "u2", data: 42 },
];
const matcher = buildFindReplaceMatcher("world", {}).regex;
const all = buildFindReplacePreview(messages, matcher, "earth", false, "all");
assert(all.length === 2 && all[0].index === 0 && all[1].index === 1, "preview picked wrong messages");
assert(all[1].after === "earth of earths" && all[1].matches.length === 2, "preview after wrong");
assert(buildFindReplacePreview(messages, matcher, "earth", false, "char").map(e => e.index).join() === "1", "target char wrong");
assert(buildFindReplacePreview(messages, matcher, "earth", false, "user").map(e => e.index).join() === "0", "target user wrong");
assert(buildFindReplacePreview(messages, matcher, "world", false, "all").length === 0, "no-op replacement listed");

// The chat changed after the preview: message 0 edited, message 1 moved.
const chat = { id: "chat", message: [
  { role: "user", chatId: "u1", data: "hello world!" },
  { role: "user", chatId: "x", data: "inserted" },
  { role: "char", chatId: "c1", data: "world of worlds" },
] };
let result = applyFindReplaceEntries(chat, all, "apply");
assert(result.changed.length === 0 && result.skipped === 2, "applied over changed or moved messages");
const fresh = { id: "chat", message: messages.map(m => Object.assign({}, m)) };
result = applyFindReplaceEntries(fresh, all, "apply");
assert(result.changed.length === 2 && fresh.message[0].data === "hello earth" && fresh.message[1].data === "earth of earths", "apply failed");
fresh.message[1].data = "edited again";
result = applyFindReplaceEntries(fresh, result.changed, "undo");
assert(result.changed.length === 1 && result.skipped === 1 && fresh.message[0].data === "hello world" && fresh.message[1].data === "edited again", "undo touched a message edited since");
`
	cmd := exec.Command(nodePath, "-e", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("find and replace fixture failed: %v\n%s", err, out)
	}
}

// Changes wait for memory per session, survive a reload through plugin
// storage, and show a notice with the button that applies them.
func TestFindReplacePendingChangesAccumulateAndClear(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for find and replace fixture")
		}
	}
	src := readArchiveCenterJS(t)
	if strings.Contains(src, "startFindReplaceNormalize") {
		t.Fatal("find and replace still starts session normalize on every replace")
	}
	functions := strings.Join([]string{
		extractArchiveCenterJSFunction(t, src, "findReplacePendingKey"),
		extractArchiveCenterJSAsyncFunction(t, src, "loadFindReplacePending"),
		extractArchiveCenterJSAsyncFunction(t, src, "addFindReplacePending"),
		extractArchiveCenterJSAsyncFunction(t, src, "clearFindReplacePending"),
		extractArchiveCenterJSFunction(t, src, "renderFindReplacePendingHtml"),
	}, "\n")
	script := `
function assert(condition, message) { if (!condition) throw new Error(message); }
const PLUGIN_ID = "p";
const store = new Map();
async function persistentGet(key) { return store.has(key) ? store.get(key) : null; }
async function persistentSet(key, value) { store.set(key, value); return true; }
async function persistentDelete(key) { store.delete(key); }
function escapeAttr(value) { return String(value); }
function t(key) { return key; }
function tf(key, vars) { return key + ":" + JSON.stringify(vars); }
let _sessionNormalizeState = { loading: false };
let _findReplacePending = new Map();
const _findReplacePendingLoading = new Set();
` + functions + `
(async () => {
  let loaded = 0;
  assert(renderFindReplacePendingHtml("s1", () => { loaded++; }) === "", "unknown state rendered a notice");
  await new Promise(r => setTimeout(r, 0));
  assert(loaded === 0, "empty state asked for a repaint");
  assert(renderFindReplacePendingHtml("s1") === "", "notice without pending changes");
  await addFindReplacePending("s1", 3);
  await addFindReplacePending("s1", 2);
  await addFindReplacePending("s2", 1);
  assert(JSON.parse(store.get("p_findReplacePending_s1")).messages === 5, "pending not accumulated");
  let html = renderFindReplacePendingHtml("s1");
  assert(html.includes('data-fr-reflect="s1"') && html.includes('"count":5') && !html.includes("disabled"), html);
  _sessionNormalizeState.loading = true;
  assert(renderFindReplacePendingHtml("s1").includes("disabled"), "button enabled while normalize runs");
  // A reload: memory is empty, plugin storage still has it.
  _findReplacePending = new Map();
  let repainted = 0;
  assert(renderFindReplacePendingHtml("s1", () => { repainted++; }) === "", "rendered before loading");
  await new Promise(r => setTimeout(r, 0));
  assert(repainted === 1 && renderFindReplacePendingHtml("s1").includes('"count":5'), "pending lost across reload");
  await clearFindReplacePending("s1");
  assert(!store.has("p_findReplacePending_s1") && renderFindReplacePendingHtml("s1") === "", "pending not cleared");
  assert(JSON.parse(store.get("p_findReplacePending_s2")).messages === 1, "another session cleared");
})().catch(err => { console.error(err); process.exitCode = 1; });
`
	cmd := exec.Command(nodePath, "-e", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("find and replace pending fixture failed: %v\n%s", err, out)
	}
}
