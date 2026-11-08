package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestFindReplaceAppliesTheSameRulesToStoredMemory(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for the find and replace fixture")
		}
	}
	src := readArchiveCenterJS(t)
	for _, want := range []string{
		`await reflectFindReplacePending(reflectSid, setFindReplaceStatus, { afterApply: true });`,
		`if (choice === "memory_text") return reflectFindReplaceMemoryText(sid, pending.rules, report);`,
		`rule: { query: st.query, replacement: st.replacement, regex: !!st.regex, caseSensitive: !!st.caseSensitive },`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("missing from the plugin: %s", want)
		}
	}
	script := `
function assert(condition, message) { if (!condition) throw new Error(message); }
const t = key => key, tf = (key, values) => key + " " + JSON.stringify(values || {});
const PLUGIN_ID = "test";
const _findReplacePending = new Map();
const store = new Map();
async function persistentGet(key) { return store.get(key) || null; }
async function persistentSet(key, value) { store.set(key, value); }
function findReplacePendingKey(sid) { return "pending_" + sid; }
const calls = [];
let previewSelection = null, fieldsResponse = null, applyResponse = null, sentApply = null, sentFields = null;
async function safeCall(fn) { return fn(); }
async function bridgeFetch(path, options) {
  calls.push(path);
  if (path === "/admin/memory-text/fields") { sentFields = options.body; return fieldsResponse; }
  if (path === "/admin/memory-text/apply") { sentApply = options.body; return applyResponse; }
  throw new Error("unexpected " + path);
}
async function reflectFindReplaceTextOnly(sid, report) { calls.push("text-only"); report("done", false); }
async function showMemoryTextReplacePreview(plan) { calls.push("preview:" + plan.changes.length + ":" + plan.renames.length); return previewSelection; }
` + extractArchiveCenterJSFunction(t, src, "buildFindReplaceMatcher") + "\n" +
		extractArchiveCenterJSFunction(t, src, "expandFindReplaceReplacement") + "\n" +
		extractArchiveCenterJSFunction(t, src, "findReplaceInText") + "\n" +
		extractArchiveCenterJSAsyncFunction(t, src, "loadFindReplacePending") + "\n" +
		extractArchiveCenterJSAsyncFunction(t, src, "addFindReplacePending") + "\n" +
		extractArchiveCenterJSAsyncFunction(t, src, "reflectFindReplaceMemoryText") + "\n" +
		extractArchiveCenterJSFunction(t, src, "buildMemoryTextReplacePlan") + "\n" +
		extractArchiveCenterJSFunction(t, src, "formatMemoryTextReplaceResult") + `
(async () => {
  // Rules are kept per replace; an undo makes them unusable.
  await addFindReplacePending("s1", 2, {query: "Mira", replacement: "Mina", regex: false, caseSensitive: true});
  await addFindReplacePending("s1", 1, {query: "Mina(\\w*)", replacement: "Minah$1", regex: true, caseSensitive: true});
  let pending = await loadFindReplacePending("s1");
  assert(pending.messages === 3 && pending.rules.length === 2 && pending.rulesComplete === true, "rules kept " + JSON.stringify(pending));
  await addFindReplacePending("s2", 1, {query: "a", replacement: "b"});
  await addFindReplacePending("s2", 1, null);
  assert((await loadFindReplacePending("s2")).rulesComplete === false, "an undo makes the rules unusable");

  fieldsResponse = {status: "ok",
    fields: [
      {ref: "memories|1|summary_json|/turn_summary", kind: "memories", turn: 3, text: "Mira waves at Rook"},
      {ref: "kg_triples|2|subject|", kind: "kg", turn: 3, text: "Miras"},
      {ref: "world_rules|3|value_json|/text", kind: "world", turn: 2, text: "Rook rules"}],
    characters: [{stable_entity_id: "e1", label: "Mira"}, {stable_entity_id: "e2", label: "Rook"}]};
  applyResponse = {status: "ok", applied: 1, stale: [], renames: [{status: "renamed"}], vector_refresh: {status: "queued"}};
  previewSelection = {kinds: new Set(["memories"]), renames: new Set(["e1"])};
  const reports = [];
  await reflectFindReplaceMemoryText("s1", pending.rules, (text, isError) => reports.push([text, isError]));
  assert(sentFields.contains === "", "several rules list every field");
  assert(JSON.stringify(calls) === JSON.stringify(["/admin/memory-text/fields", "preview:2:1", "text-only", "/admin/memory-text/apply"]), "order " + JSON.stringify(calls));
  assert(sentApply.changes.length === 1 && sentApply.changes[0].after === "Minah waves at Rook" && sentApply.changes[0].before === "Mira waves at Rook", "only the chosen kind, both rules applied in order: " + JSON.stringify(sentApply.changes));
  assert(JSON.stringify(sentApply.renames) === JSON.stringify([{stable_entity_id: "e1", to: "Minah"}]), "renames " + JSON.stringify(sentApply.renames));
  const last = reports[reports.length - 1];
  assert(last[1] === false && last[0].includes("findReplace.memoryDone") && last[0].includes("findReplace.memoryVectorsQueued"), "report " + JSON.stringify(reports));

  // Cancelling the preview stores nothing, not even the chat text.
  calls.length = 0; sentApply = null; previewSelection = null;
  await reflectFindReplaceMemoryText("s1", [{query: "Mira", replacement: "Mina", regex: false, caseSensitive: false}], () => {});
  assert(sentFields.contains === "Mira" && sentFields.case_sensitive === false, "a plain rule narrows the listing");
  assert(sentApply === null && !calls.includes("text-only"), "cancel wrote " + JSON.stringify(calls));
  process.stdout.write("ok");
})().catch(err => { console.error(err.message); process.exit(1); });
`
	cmd := exec.Command(nodePath, "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("find and replace memory fixture failed: %v\n%s", err, out)
	}
}
