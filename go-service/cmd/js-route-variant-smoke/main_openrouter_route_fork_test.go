package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// OpenRouter provider pinning: the setting becomes provider.order with no
// fallbacks inside the extra body, only for the OpenRouter provider, and keeps
// the rest of the extra body.
func TestOpenRouterProviderRouteMergesIntoExtraBody(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for the OpenRouter route fixture")
		}
	}
	src := readArchiveCenterJS(t)
	for _, required := range []string{
		`syncProviderSpecificRow("mo-pluginMainProvider", "mo-pluginMainOpenRouterProviderRouteRow", "openrouter");`,
		`syncProviderSpecificRow("mo-subLlmProvider", "mo-subLlmOpenRouterProviderRouteRow", "openrouter");`,
		`isSub ? settings.subLlmOpenRouterProviderRoute : settings.pluginMainOpenRouterProviderRoute,`,
		`isSub ? cfg.subLlmOpenRouterProviderRoute : cfg.pluginMainOpenRouterProviderRoute,`,
		`syncProviderSpecificRow("mo-backupProvider", "mo-backupOpenRouterProviderRouteRow", "openrouter");`,
		`backupOpenRouterProviderRoute: typeof s.backupOpenRouterProviderRoute === "string" ? s.backupOpenRouterProviderRoute.trim() : "",`,
	} {
		if !strings.Contains(src, required) {
			t.Fatalf("Archive Center.js missing %q", required)
		}
	}
	script := strings.Join([]string{
		`function normalizeLlmProvider(value, fallback) { return String(value || fallback).trim().toLowerCase(); }`,
		extractArchiveCenterJSFunction(t, src, "forkOpenRouterProviderSlugs"),
		extractArchiveCenterJSFunction(t, src, "forkSanitizeOpenRouterProviderRoute"),
		extractArchiveCenterJSFunction(t, src, "forkExtraBodyWithProviderRoute"),
	}, "\n") + `
function assert(condition, message) { if (!condition) throw new Error(message); }
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
assert(forkSanitizeOpenRouterProviderRoute(" google-vertex ,, deepinfra ") === "google-vertex, deepinfra", "sanitize");
assert(forkExtraBodyWithProviderRoute("", "google-vertex", "openrouter") === JSON.stringify({ provider: { order: ["google-vertex"], allow_fallbacks: false } }), "empty extra body");
assert(forkExtraBodyWithProviderRoute("  \n", "google-vertex", "openrouter") === JSON.stringify({ provider: { order: ["google-vertex"], allow_fallbacks: false } }), "blank extra body");
const merged = JSON.parse(forkExtraBodyWithProviderRoute('{"top_k":5,"provider":{"sort":"price","order":["x"]}}', "a, b", "OpenRouter"));
assert(same(merged, { top_k: 5, provider: { sort: "price", order: ["a", "b"], allow_fallbacks: false } }), "merge " + JSON.stringify(merged));
assert(forkExtraBodyWithProviderRoute('{"top_k":5}', "a", "openai") === '{"top_k":5}', "other providers untouched");
assert(forkExtraBodyWithProviderRoute('{"top_k":5}', " , ", "openrouter") === '{"top_k":5}', "empty route untouched");
assert(forkExtraBodyWithProviderRoute("{bad", "a", "openrouter") === "{bad", "invalid JSON left for the backend to report");
assert(forkExtraBodyWithProviderRoute("[1]", "a", "openrouter") === "[1]", "non-object left alone");
`
	cmd := exec.Command(nodePath, "-e", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("OpenRouter route fixture failed: %v\n%s", err, out)
	}
}
