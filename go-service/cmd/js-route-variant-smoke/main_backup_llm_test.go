package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestBackupLLMRoleSwitchesRenderAndSync(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for backup LLM settings fixture")
		}
	}
	src := readArchiveCenterJS(t)
	rolesStart := strings.Index(src, "const BACKUP_LLM_ROLES = [")
	if rolesStart < 0 {
		t.Fatal("Archive Center.js has no BACKUP_LLM_ROLES list")
	}
	roles := src[rolesStart : rolesStart+strings.Index(src[rolesStart:], "];")+2]
	functions := strings.Join([]string{
		extractArchiveCenterJSFunction(t, src, "backupLlmRoleLabel"),
		extractArchiveCenterJSFunction(t, src, "renderBackupLlmRoleChecks"),
	}, "\n")
	for _, want := range []string{
		`backupLlmRoles: Array.isArray(s.backupLlmRoles) ? s.backupLlmRoles.slice() : [],`,
		`backupLlmEnabled: s.backupLlmEnabled === true,`,
		`backupTemperatureFromRole: s.backupTemperatureFromRole !== false,`,
		`backupTimeoutFromRole: s.backupTimeoutFromRole !== false,`,
	} {
		if !strings.Contains(extractJSFunctionBlockForTest(t, src, "async function syncConfigToBackend(s)"), want) {
			t.Fatalf("syncConfigToBackend does not send %s", want)
		}
	}
	for _, want := range []string{`"backup_role=publisher"`, `"backup_role=critic"`} {
		if !strings.Contains(src, want) {
			t.Fatalf("plugin-side LLM calls do not name their role: %s", want)
		}
	}
	script := `
function assert(condition, message) { if (!condition) throw new Error(message); }
function t(key) { return key; }
function escapeAttr(value) { return String(value == null ? "" : value).replace(/"/g, "&quot;"); }
` + roles + "\n" + functions + `
assert(BACKUP_LLM_ROLES.length === 14, "every LLM role has a switch");
assert(BACKUP_LLM_ROLES.includes("publisher") && BACKUP_LLM_ROLES.includes("preprocessing.world_state"), "publisher and preprocessing roles are listed");
const html = renderBackupLlmRoleChecks({backupLlmRoles: ["publisher", "preprocessing.world_state"]});
assert((html.match(/type="checkbox"/g) || []).length === 14, "one checkbox per role");
assert(html.includes('data-backup-role="publisher" checked'), "enabled role is checked");
assert(!html.includes('data-backup-role="critic" checked'), "other roles stay off");
assert(html.includes('id="mo-backupRole-preprocessing-world_state"'), "ids avoid dots");
assert(backupLlmRoleLabel("preprocessing.world_state") === "backupRole.preprocessing · turn_hud.preprocessing.world_state", "preprocessing roles reuse the HUD role names");
process.stdout.write("ok");
`
	cmd := exec.Command(nodePath, "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("backup LLM settings fixture failed: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("backup LLM settings fixture output=%q, want ok", out)
	}
}
