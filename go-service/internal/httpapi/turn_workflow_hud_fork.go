package httpapi

import (
	"sort"
	"strings"
	"time"
)

// addBackupLLMWarning records that the backup model answered for roles, under
// the stage running at that moment, once per stage and set of roles.
func (l *turnWorkflowHUDLedger) addBackupLLMWarning(requestID string, roles []string, reason string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.entries[strings.TrimSpace(requestID)]
	if entry == nil || turnWorkflowHUDTerminal(entry.view.Status) {
		return
	}
	notice := turnWorkflowHUDNotice{
		Code: "BACKUP_LLM_USED", MessageKey: "turn_hud.warning.backup_llm_used",
		BackupRoles: append([]string(nil), roles...), BackupReason: strings.TrimSpace(reason),
	}
	if stage := entry.view.CurrentStage; stage != nil && stage.Status == "running" {
		notice.StageKey = stage.Key
	}
	sort.Strings(notice.BackupRoles)
	for _, existing := range entry.view.Warnings {
		if existing.Code == notice.Code && existing.StageKey == notice.StageKey &&
			strings.Join(existing.BackupRoles, ",") == strings.Join(notice.BackupRoles, ",") {
			return
		}
	}
	entry.view.Warnings = append(entry.view.Warnings, notice)
	entry.view.Severity = turnWorkflowHUDSeverityWarning
	l.touchLocked(entry, time.Now().UTC())
}
