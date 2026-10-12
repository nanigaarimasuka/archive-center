package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The full HUD names the stage, roles and reason of each backup answer; the
// compact HUD only marks the stage on show when the backup answered in it.
func TestTurnHUDShowsWhereTheBackupModelAnswered(t *testing.T) {
	nodePath := strings.TrimSpace(os.Getenv("ARCHIVE_CENTER_NODE_BINARY"))
	if nodePath == "" {
		var err error
		nodePath, err = exec.LookPath("node")
		if err != nil {
			t.Skip("node is required for backup HUD fixture")
		}
	}
	src := readArchiveCenterJS(t)
	for _, required := range []string{
		`if (item.code === "BACKUP_LLM_USED") message = [message].concat(turnWorkflowHUDBackupDetails(view, item)).join(" · ");`,
		`const backupBadge = !view.host_generation_finished && turnWorkflowHUDStageUsedBackup(view, stage.key)`,
		`+ backupBadge`,
		`"turn_hud.compact.backup_used": "예비 모델 호출됨",`,
	} {
		if !strings.Contains(src, required) {
			t.Fatalf("Archive Center.js missing %q", required)
		}
	}
	functions := strings.Join([]string{
		extractArchiveCenterJSFunction(t, src, "turnWorkflowHUDBackupDetails"),
		extractArchiveCenterJSFunction(t, src, "turnWorkflowHUDStageUsedBackup"),
	}, "\n")
	script := `
function assert(condition, message) { if (!condition) throw new Error(message); }
const dict = {
  "turn_hud.stage.context_assembly": "컨텍스트 조립",
  "turn_hud.stage.publisher_llm": "출판사",
  "backupRole.preprocessing": "전처리",
  "backupRole.publisher": "출판사 (입력 편집)",
  "turn_hud.preprocessing.world_state": "세계·사물",
  "turn_hud.backup_reason.refusal_text": "거부 문구",
};
function t(key) { return dict[key] || key; }
` + functions + `
const view = {
  stages: [{ key: "context_assembly", label_key: "turn_hud.stage.context_assembly" }, { key: "publisher_llm", label_key: "turn_hud.stage.publisher_llm" }],
  warnings: [
    { code: "BACKUP_LLM_USED", stage_key: "context_assembly", backup_roles: ["preprocessing.world_state", "preprocessing.new_role"], backup_reason: "refusal_text" },
    { code: "OTHER", stage_key: "publisher_llm" },
  ],
};
const details = turnWorkflowHUDBackupDetails(view, view.warnings[0]).join(" · ");
assert(details === "컨텍스트 조립 · 전처리(세계·사물), 전처리(new_role) · 거부 문구", details);
assert(turnWorkflowHUDBackupDetails(view, { code: "BACKUP_LLM_USED", backup_roles: ["publisher"], backup_reason: "odd" }).join(" · ") === "출판사 (입력 편집) · odd", "fallbacks wrong");
assert(turnWorkflowHUDBackupDetails(view, { code: "BACKUP_LLM_USED" }).length === 0, "empty warning got details");
assert(turnWorkflowHUDStageUsedBackup(view, "context_assembly") === true, "stage with a backup answer not marked");
assert(turnWorkflowHUDStageUsedBackup(view, "publisher_llm") === false, "another warning marked the stage");
assert(turnWorkflowHUDStageUsedBackup(view, "") === false, "no stage marked");
`
	cmd := exec.Command(nodePath, "-e", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("backup HUD fixture failed: %v\n%s", err, out)
	}
}
