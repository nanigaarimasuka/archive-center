package httpapi

import (
	"context"
	"testing"
)

// The HUD shows which stage the backup answered in, for which roles and why,
// once per stage and set of roles.
func TestBackupLLMWarningNamesStageRolesAndReason(t *testing.T) {
	ledger := newTurnWorkflowHUDLedger()
	ledger.begin("req", "sess", 1)
	srv := &Server{TurnWorkflows: ledger}
	notice, ok := srv.withBackupLLMWorkflowNotice(context.Background(), "req").Value(backupLLMNoticeKey{}).(func(map[string]any))
	if !ok {
		t.Fatal("no notice on the context")
	}

	ledger.startStage("req", turnWorkflowStageContext)
	notice(map[string]any{"roles": []string{"preprocessing.world_state"}, "reason": "refusal_text"})
	notice(map[string]any{"roles": []string{"preprocessing.world_state"}, "reason": "refusal_text"})
	notice(map[string]any{"roles": []string{"preprocessing.unresolved_goal", "preprocessing.event_recent"}, "reason": "missing_json"})
	ledger.startStage("req", turnWorkflowStagePublisherLLM)
	notice(map[string]any{"roles": []string{"publisher"}, "reason": "provider_content_policy"})

	view, found := ledger.snapshot("req")
	if !found {
		t.Fatal("no HUD entry")
	}
	want := []turnWorkflowHUDNotice{
		{Code: "BACKUP_LLM_USED", MessageKey: "turn_hud.warning.backup_llm_used", StageKey: turnWorkflowStageContext, BackupRoles: []string{"preprocessing.world_state"}, BackupReason: "refusal_text"},
		{Code: "BACKUP_LLM_USED", MessageKey: "turn_hud.warning.backup_llm_used", StageKey: turnWorkflowStageContext, BackupRoles: []string{"preprocessing.event_recent", "preprocessing.unresolved_goal"}, BackupReason: "missing_json"},
		{Code: "BACKUP_LLM_USED", MessageKey: "turn_hud.warning.backup_llm_used", StageKey: turnWorkflowStagePublisherLLM, BackupRoles: []string{"publisher"}, BackupReason: "provider_content_policy"},
	}
	if len(view.Warnings) != len(want) {
		t.Fatalf("warnings = %+v", view.Warnings)
	}
	for i, w := range want {
		got := view.Warnings[i]
		if got.Code != w.Code || got.MessageKey != w.MessageKey || got.StageKey != w.StageKey || got.BackupReason != w.BackupReason ||
			len(got.BackupRoles) != len(w.BackupRoles) {
			t.Fatalf("warning %d = %+v, want %+v", i, got, w)
		}
		for j := range w.BackupRoles {
			if got.BackupRoles[j] != w.BackupRoles[j] {
				t.Fatalf("warning %d roles = %v, want %v", i, got.BackupRoles, w.BackupRoles)
			}
		}
	}
	if view.Severity != turnWorkflowHUDSeverityWarning {
		t.Fatalf("severity = %q", view.Severity)
	}
}
