package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestBridgeFetchGzipsRequestBodiesFromOneKilobyte(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for bridge gzip fixture")
		}
	}
	src := readArchiveCenterJS(t)
	bridge := extractArchiveCenterJSAsyncFunction(t, src, "bridgeFetch")
	for _, want := range []string{
		`if (!rawBody && !webDirectBridge && typeof fetchInit.body === "string") {`,
		`mergedHeaders["Content-Encoding"] = "gzip";`,
	} {
		if !strings.Contains(bridge, want) {
			t.Fatalf("bridgeFetch is missing %q", want)
		}
	}
	script := `
const zlib = require("node:zlib");
function assert(condition, message) { if (!condition) throw new Error(message); }
const BRIDGE_GZIP_MIN_BYTES = 1024;
` + extractArchiveCenterJSAsyncFunction(t, src, "gzipBridgeRequestBody") + `
(async function() {
  assert(await gzipBridgeRequestBody("x".repeat(1023)) === null, "a body under 1KB was compressed");
  const text = JSON.stringify({entries: Array.from({length: 40}, (_, i) => ({turn_index: i, assistant_content: "같은 문장 " + i}))});
  const packed = await gzipBridgeRequestBody(text);
  assert(packed instanceof Uint8Array && packed.length < new TextEncoder().encode(text).length, "a large body was not compressed");
  assert(zlib.gunzipSync(Buffer.from(packed)).toString("utf8") === text, "the compressed body does not unpack to the original");
  process.stdout.write("ok");
})().catch(function(err) { console.error(err && err.stack || err); process.exit(1); });
`
	cmd := exec.Command(nodePath, "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bridge gzip fixture failed: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("bridge gzip fixture output=%q, want ok", out)
	}
}
