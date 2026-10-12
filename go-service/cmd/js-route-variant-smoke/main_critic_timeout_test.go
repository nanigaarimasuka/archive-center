package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestCriticAndPublisherTimeoutsAcceptUpToFifteenMinutes(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for the critic timeout fixture")
		}
	}
	src := readArchiveCenterJS(t)
	for _, input := range []string{
		`id="mo-subLlmTimeoutMs" value="${s.subLlmTimeoutMs ?? DEFAULT_SETTINGS.subLlmTimeoutMs}" min="5000" max="900000"`,
		`id="mo-pluginMainTimeoutMs" value="${s.pluginMainTimeoutMs ?? DEFAULT_SETTINGS.pluginMainTimeoutMs}" min="5000" max="900000"`,
	} {
		if !strings.Contains(src, input) {
			t.Fatalf("a timeout input still stops below 900000 ms: %s", input)
		}
	}
	script := `
function assert(condition, message) { if (!condition) throw new Error(message); }
const DEFAULT_SETTINGS = {subLlmTimeoutMs: 120000, pluginMainTimeoutMs: 120000};
let settings = {};
` + extractArchiveCenterJSFunction(t, src, "sanitizeNumber") + "\n" + extractArchiveCenterJSFunction(t, src, "getSubLlmTimeoutSettingMs") + "\n" + extractArchiveCenterJSFunction(t, src, "getPluginMainTimeoutSettingMs") + `
assert(getSubLlmTimeoutSettingMs(600000) === 600000, "10 minutes kept: " + getSubLlmTimeoutSettingMs(600000));
assert(getSubLlmTimeoutSettingMs(900000) === 900000, "15 minutes kept");
assert(getSubLlmTimeoutSettingMs(900001) === 120000, "past the cap falls back to the default");
assert(getPluginMainTimeoutSettingMs(600000) === 600000, "publisher 10 minutes kept");
process.stdout.write("ok");
`
	cmd := exec.Command(nodePath, "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("critic timeout fixture failed: %v\n%s", err, out)
	}
}
