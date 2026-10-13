package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The plugin keeps a copy of each chat and asks Risu only for what changed;
// the chat it rebuilds must equal a whole copy, and callers must not be able
// to change the kept copy.
func TestHostChatCopyRebuildsTheChatFromChanges(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for host chat copy fixture")
		}
	}
	src := readArchiveCenterJS(t)
	for _, required := range []string{
		"const _hostChatCopies = new Map();",
		"const HOST_CHAT_COPY_MAX = 4;",
		"const chat = await readHostChatFromIndex(charIdx, chatIdx);",
	} {
		if !strings.Contains(src, required) {
			t.Fatalf("Archive Center.js missing %q", required)
		}
	}
	functions := strings.Join([]string{
		extractArchiveCenterJSFunction(t, src, "copyHostChatValue"),
		extractArchiveCenterJSAsyncFunction(t, src, "readHostChatFromIndex"),
	}, "\n")
	script := `
function assert(condition, message) { if (!condition) throw new Error(message); }
// Risu's side (chatChanges.ts): versions per object, copies of what is new.
const seen = new WeakMap();
let nextVersion = 1;
let epoch = "e1";
function contentVersion(value) {
  const keys = Object.keys(value);
  const values = keys.map(key => value[key] !== null && typeof value[key] === "object" ? {child: contentVersion(value[key])} : value[key]);
  const previous = seen.get(value);
  if (previous && previous.keys.length === keys.length && previous.keys.every((key, i) => key === keys[i]
      && (typeof values[i] === "object" && values[i] && typeof previous.values[i] === "object" && previous.values[i]
        ? values[i].child === previous.values[i].child : Object.is(values[i], previous.values[i])))) {
    return previous.version;
  }
  const version = nextVersion++;
  seen.set(value, {keys, values, version});
  return version;
}
const host = {id: "chat-1", name: "Chat", message: [{role: "user", data: "a"}, {role: "char", data: "b", info: {model: "m"}}], scriptstate: {x: 1}};
const sent = [];
const R = {
  async getChatChangesFromIndex(charIdx, chatIdx, known) {
    const base = known && known.epoch === epoch && known.chatId === host.id ? known : null;
    const knownMessages = new Set(base ? base.messages : []);
    const knownFields = base ? base.fields : {};
    const out = {epoch, chatId: host.id, keys: Object.keys(host), messageArray: true, messageVersions: [], messages: {}, fieldVersions: {}, fields: {}};
    for (const key of out.keys) {
      if (key === "message") {
        host.message.forEach((message, i) => {
          const version = contentVersion(message);
          out.messageVersions.push(version);
          if (!knownMessages.has(version)) out.messages[i] = structuredClone(message);
        });
      } else if (host[key] !== null && typeof host[key] === "object") {
        const version = contentVersion(host[key]);
        out.fieldVersions[key] = version;
        if (knownFields[key] !== version) out.fields[key] = structuredClone(host[key]);
      } else {
        out.fields[key] = host[key];
      }
    }
    sent.push(Object.keys(out.messages).length);
    return structuredClone(out);
  },
  async getChatFromIndex() { throw new Error("whole copy not expected"); },
};
` + functions + `
(async () => {
  const first = await readHostChatFromIndex(0, 0);
  assert(JSON.stringify(first) === JSON.stringify(host), "first read differs from the chat");
  assert(sent[0] === 2, "first read must copy every message");
  first.message[0].data = "changed by a caller";
  first.message.push({role: "x"});
  const again = await readHostChatFromIndex(0, 0);
  assert(JSON.stringify(again) === JSON.stringify(host), "a caller changed the kept copy");
  assert(sent[1] === 0, "unchanged chat must copy no message");
  host.message[1].info.model = "n";
  host.message.splice(0, 0, {role: "user", data: "new first"});
  host.message.push({role: "char", data: "c"});
  host.scriptstate.x = 2;
  const changed = await readHostChatFromIndex(0, 0);
  assert(JSON.stringify(changed) === JSON.stringify(host), "changed chat rebuilt wrong");
  assert(sent[2] === 3, "only the new and changed messages may be copied, got " + sent[2]);
  epoch = "e2";
  const reloaded = await readHostChatFromIndex(0, 0);
  assert(JSON.stringify(reloaded) === JSON.stringify(host) && sent[3] === host.message.length, "a new epoch must copy everything");
})().catch(err => { console.error(err); process.exitCode = 1; });
`
	cmd := exec.Command(nodePath, "-e", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("host chat copy fixture failed: %v\n%s", err, out)
	}
}
