package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestViewSessionCheckMatchesTheFullCacheCheckWithoutCopyingTheChat(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for view session fixture")
		}
	}
	src := readArchiveCenterJS(t)
	functions := strings.Join([]string{
		extractArchiveCenterJSAsyncFunction(t, src, "getCurrentChatSessionIdForView"),
		extractArchiveCenterJSAsyncFunction(t, src, "refreshSessionDisplayLookupFromRuntime"),
	}, "\n")
	script := `
function assert(condition, message) { if (!condition) throw new Error(message); }
let fullChecks = 0;
let characterCopies = 0;
let primed = [];
let current = {charIdx: 2, chatIdx: 0, chatId: "abc", characterId: "cha-1"};
const cache = {sessionId: "char_2_cid_abc", charIdx: 2, chatIdx: 0, stableCharacterId: "cha-1", observedChatUniqueId: "abc", latestUserHash: "u", latestAssistantHash: "a", completedTurnCount: 7};
let _sessionCache = cache;
const cacheBefore = JSON.stringify(cache);
const R = {
  getCurrentChatIndex: async () => current.chatIdx,
  getCurrentCharacterIndex: async () => current.charIdx,
  getChatIdentityFromIndex: async (charIdx, chatIdx) => (charIdx === current.charIdx && chatIdx === current.chatIdx ? {chatId: current.chatId, characterId: current.characterId} : null),
  getChatSummaries: async () => [{characterId: "cha-1", name: "Alice", chats: [{id: "abc", name: "Day one"}]}],
  getCharacter: async () => { characterCopies++; return {name: "Alice", chats: []}; },
};
async function getCurrentChatSessionId() { fullChecks++; return "full-check"; }
function debugLog() {}
async function loadSessionDisplayLookupCacheFromPersistent() {}
function getRisuCharacterListSnapshot() { return []; }
function clearRuntimeChatSessionInventory() {}
function primeSessionDisplayLookupFromCharacters(list) { primed = list; return true; }
const _runtimeChatSessionInventory = {};
` + functions + `
(async function() {
  assert(await getCurrentChatSessionIdForView() === "char_2_cid_abc" && fullChecks === 0, "a matching cache was not reused");
  assert(JSON.stringify(_sessionCache) === cacheBefore, "the view check rewrote the cache the turn flow reads");

  for (const change of [{chatId: "other"}, {characterId: "cha-2"}, {chatIdx: 1}, {charIdx: 3}]) {
    const saved = current;
    current = Object.assign({}, current, change);
    fullChecks = 0;
    assert(await getCurrentChatSessionIdForView() === "full-check" && fullChecks === 1, "a changed " + Object.keys(change)[0] + " did not run the full check");
    current = saved;
  }

  _sessionCache = Object.assign({}, cache, {sessionId: "migrated-session"});
  fullChecks = 0;
  assert(await getCurrentChatSessionIdForView() === "full-check", "a session the backend mapped elsewhere must take the full check, as it does today");
  _sessionCache = cache;

  const getChatIdentityFromIndex = R.getChatIdentityFromIndex;
  delete R.getChatIdentityFromIndex;
  fullChecks = 0;
  assert(await getCurrentChatSessionIdForView() === "full-check", "without the Risu API the full check must run");
  R.getChatIdentityFromIndex = getChatIdentityFromIndex;

  await refreshSessionDisplayLookupFromRuntime();
  assert(characterCopies === 0 && primed[0].chats[0].name === "Day one", "session labels copied the character instead of reading the summaries");
  delete R.getChatSummaries;
  await refreshSessionDisplayLookupFromRuntime();
  assert(characterCopies === 1, "without the summaries API the labels must fall back to the current character");
  process.stdout.write("ok");
})().catch(function(err) { console.error(err && err.stack || err); process.exit(1); });
`
	cmd := exec.Command(nodePath, "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("view session fixture failed: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("view session fixture output=%q, want ok", out)
	}
}
