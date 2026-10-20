package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Saving only the backend address keeps this device's save time and never
// pushes settings, so the backend's shared copy is what comes back.
func TestSaveBackendAddressOnlyKeepsTheSharedSettings(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for backend address fixture")
		}
	}
	src := readArchiveCenterJS(t)
	saveOnly := extractArchiveCenterJSAsyncFunction(t, src, "saveBackendAddressOnly")
	if strings.Contains(saveOnly, "settingsSavedAt") || strings.Contains(saveOnly, "pushSettingsToBackend") {
		t.Fatal("saving the address alone touches the save time or pushes settings")
	}
	if !strings.Contains(src, `id="mo-save-backend-address"`) {
		t.Fatal("settings panel has no address-only save button")
	}
	script := saveOnly + `
function assert(condition, message) { if (!condition) throw new Error(message); }
const SETTINGS_KEY = "k";
let settings = { bridgeUrl: "http://127.0.0.1:28080", webDirectBridgeEnabled: false, settingsSavedAt: 0, criticModel: "" };
const writes = [];
let restoreAnswer = true, syncs = 0, bridges = 0, dirty = 0;
function isValidBridgeUrlInput(url) { return /^https?:\/\//.test(url); }
function sanitizeSettings(raw) { return Object.assign({}, raw); }
async function persistentSet(key, value) { writes.push([key, JSON.parse(value)]); }
async function restoreSettingsFromBackend() {
  if (restoreAnswer) settings = Object.assign({}, settings, { criticModel: "shared", settingsSavedAt: 500 });
  return restoreAnswer;
}
function markBackendRuntimeConfigDirty() { dirty++; }
async function syncConfigToBackend() { syncs++; return { ok: true }; }
function startRisuModelBridge() { bridges++; }
(async () => {
  assert((await saveBackendAddressOnly("not a url", true)).ok === false && writes.length === 0, "invalid address saved");
  let result = await saveBackendAddressOnly("https://example.test/ac", true);
  assert(result.ok && result.restored, "restore not reported");
  assert(writes.length === 1 && writes[0][1].bridgeUrl === "https://example.test/ac" && writes[0][1].webDirectBridgeEnabled === true, "address not saved");
  assert(writes[0][1].settingsSavedAt === 0, "save time changed by an address-only save");
  assert(settings.criticModel === "shared" && syncs === 1 && bridges === 1 && dirty === 1, "restored settings not applied to the backend runtime");
  restoreAnswer = false;
  result = await saveBackendAddressOnly("https://example.test/ac2", false);
  assert(result.ok && !result.restored && syncs === 1, "synced runtime without a restore");
})().catch(err => { console.error(err); process.exitCode = 1; });
`
	cmd := exec.Command(nodePath, "-e", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("backend address fixture failed: %v\n%s", err, out)
	}
}
