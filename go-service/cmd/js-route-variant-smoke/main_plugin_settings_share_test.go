package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestPluginSettingsAreSharedAcrossDevicesThroughTheBackend(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for plugin settings sharing fixture")
		}
	}
	src := readArchiveCenterJS(t)
	initSource := extractJSFunctionBlockForTest(t, src, "async function init()")
	restoreIndex := strings.Index(initSource, `await restoreSettingsFromBackend()`)
	syncIndex := strings.Index(initSource, `const syncAck = await syncConfigToBackend(settings)`)
	if restoreIndex < 0 || syncIndex < 0 || restoreIndex > syncIndex {
		t.Fatal("settings from another device are not restored before the runtime sync")
	}
	functions := strings.Join([]string{
		extractArchiveCenterJSFunction(t, src, "backendSharedSettings"),
		extractArchiveCenterJSAsyncFunction(t, src, "pushSettingsToBackend"),
		extractArchiveCenterJSAsyncFunction(t, src, "restoreSettingsFromBackend"),
	}, "\n")
	script := `
const DEVICE_LOCAL_SETTING_KEYS = Object.freeze(["bridgeUrl", "webDirectBridgeEnabled"]);
const SETTINGS_KEY = "settings";
let settings;
let _settingsStorageStatus;
let backend;
let puts;
let persisted;
async function bridgeFetch(path, options) {
  if (path !== "/config/plugin-settings") throw new Error("unexpected path " + path);
  if (!backend) return null;
  if (options.method === "PUT") { puts.push(options.body); backend.settings = options.body; return {status:"ok"}; }
  return {status:"ok", settings: backend.settings};
}
async function safeCall(fn, fallback) { try { return await fn(); } catch { return fallback; } }
function getRequestTimeoutSettingMs() { return 1000; }
function sanitizeSettings(raw) { return {...raw}; }
async function persistentSet(key, value) { persisted.push(JSON.parse(value)); }
function warnLog() {}
function debugLog() {}
function assert(condition, message) { if (!condition) throw new Error(message); }
function reset(local, remote, mode) {
  settings = local;
  backend = remote === undefined ? null : {settings: remote};
  _settingsStorageStatus = {mode: mode || "device_local"};
  puts = [];
  persisted = [];
}
` + functions + `
(async function() {
  // A phone opens with older settings: the computer's newer settings win,
  // but the phone keeps its own backend address.
  reset({bridgeUrl:"https://phone/ac", webDirectBridgeEnabled:true, maxInjectionChars:32000, settingsSavedAt:1},
    {bridgeUrl:"http://127.0.0.1:28080", maxInjectionChars:12000, settingsSavedAt:2});
  assert(await restoreSettingsFromBackend() === true, "newer backend settings were not applied");
  assert(settings.maxInjectionChars === 12000 && settings.settingsSavedAt === 2, "backend settings were not applied");
  assert(settings.bridgeUrl === "https://phone/ac" && settings.webDirectBridgeEnabled === true, "device backend address was replaced");
  assert(persisted.length === 1 && persisted[0].maxInjectionChars === 12000 && puts.length === 0, "restored settings were not kept locally");

  // Settings saved while the backend was unreachable are newer and go up.
  reset({bridgeUrl:"https://pc/ac", maxInjectionChars:9000, settingsSavedAt:3}, {maxInjectionChars:12000, settingsSavedAt:2});
  assert(await restoreSettingsFromBackend() === false, "older backend settings replaced newer local ones");
  assert(settings.maxInjectionChars === 9000 && puts.length === 1 && puts[0].maxInjectionChars === 9000, "newer local settings were not shared");
  assert(!("bridgeUrl" in puts[0]) && !("webDirectBridgeEnabled" in puts[0]), "device backend address was shared");

  // The same settings are left alone.
  reset({maxInjectionChars:9000, settingsSavedAt:3}, {maxInjectionChars:9000, settingsSavedAt:3});
  assert(await restoreSettingsFromBackend() === false && puts.length === 0 && persisted.length === 0, "unchanged settings were written");

  // An empty backend is seeded from settings saved before sharing existed.
  reset({maxInjectionChars:7000}, null);
  await restoreSettingsFromBackend();
  assert(settings.settingsSavedAt > 0 && persisted.length === 1 && puts.length === 1 && puts[0].maxInjectionChars === 7000, "saved settings did not seed the backend");

  // Defaults a device never saved never replace another device's settings.
  reset({maxInjectionChars:32000}, null, "default");
  await restoreSettingsFromBackend();
  assert(puts.length === 0 && persisted.length === 0, "unsaved defaults were shared");

  // An unreachable backend leaves local settings in place.
  reset({maxInjectionChars:5000, settingsSavedAt:1});
  assert(await restoreSettingsFromBackend() === false && settings.maxInjectionChars === 5000, "unreachable backend changed settings");
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
		t.Fatalf("plugin settings sharing JS fixture failed: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("plugin settings sharing JS fixture output=%q, want ok", out)
	}
}
