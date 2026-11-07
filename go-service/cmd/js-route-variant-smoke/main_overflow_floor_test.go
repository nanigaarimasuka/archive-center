package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestMemoryOverflowMinRelevanceIsASyncedSetting(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for the overflow floor fixture")
		}
	}
	src := readArchiveCenterJS(t)
	for _, want := range []string{
		`memoryOverflowMinRelevance: 0.3,`,
		`memoryOverflowMinRelevance: sanitizeMemoryOverflowMinRelevance(s.memoryOverflowMinRelevance),`,
		`id="mo-memoryOverflowMinRelevance"`,
		`memoryOverflowMinRelevance: $("mo-memoryOverflowMinRelevance")`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("missing from the plugin: %s", want)
		}
	}
	script := `
function assert(condition, message) { if (!condition) throw new Error(message); }
const DEFAULT_SETTINGS = {memoryOverflowMinRelevance: 0.3};
` + extractArchiveCenterJSFunction(t, src, "sanitizeMemoryOverflowMinRelevance") + `
assert(sanitizeMemoryOverflowMinRelevance("0.45") === 0.45, "a typed value is kept");
assert(sanitizeMemoryOverflowMinRelevance(0) === 0, "0 turns the floor off");
assert(sanitizeMemoryOverflowMinRelevance(1.2) === 0.3, "out of range falls back");
assert(sanitizeMemoryOverflowMinRelevance("x") === 0.3, "not a number falls back");
assert(sanitizeMemoryOverflowMinRelevance(undefined) === 0.3, "missing falls back");
process.stdout.write("ok");
`
	cmd := exec.Command(nodePath, "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("overflow floor fixture failed: %v\n%s", err, out)
	}
}
