package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/risulongmemory/archive-center-go/internal/dto"
)

// The backup LLM answers once in place of a role's model when that model
// refuses the content: a provider safety stop, a refusal message, an empty
// answer, or prose where JSON was expected. Connection, timeout, and key
// failures stay with the primary model's own retries.

const (
	backupRolePublisher        = "publisher"
	backupRoleCritic           = "critic"
	backupRoleWorldRuleAudit   = "world_rule_audit"
	backupRoleChapter          = "chapter"
	backupRoleArc              = "arc"
	backupRoleSaga             = "saga"
	backupRoleSourceSearch     = "source_search"
	backupRoleReferenceLibrary = "reference_library"
	backupRoleMemoryImport     = "memory_import"
	backupRolePreprocessing    = "preprocessing."
)

type backupLLMTarget struct {
	Roles           []string
	Provider        string
	APIKey          string
	Endpoint        string
	Model           string
	TimeoutMs       int64
	Temperature     *float64
	MaxTokens       *int64
	ReasoningPreset string
	ReasoningEffort string
	ReasoningBudget *int64
}

// backupLLMFor returns the backup for a call made for the given roles, or nil
// when the backup is off, incomplete, or not switched on for any of them. A
// call shared by several preprocessing roles uses it if any of them wants it.
func (s *Server) backupLLMFor(roles ...string) *backupLLMTarget {
	if s == nil {
		return nil
	}
	rt := s.runtimeConfigSnapshot()
	if !rt.BackupEnabled {
		return nil
	}
	provider := strings.ToLower(strings.TrimSpace(rt.BackupProvider))
	model := strings.TrimSpace(rt.BackupModel)
	if provider == "" || model == "" || (strings.TrimSpace(rt.BackupAPIKey) == "" && llmProviderNeedsAPIKey(provider)) {
		return nil
	}
	enabled := []string{}
	for _, role := range roles {
		if rt.BackupRoles[role] {
			enabled = append(enabled, role)
		}
	}
	if len(enabled) == 0 {
		return nil
	}
	target := &backupLLMTarget{
		Roles:           enabled,
		Provider:        provider,
		APIKey:          strings.TrimSpace(rt.BackupAPIKey),
		Endpoint:        strings.TrimSpace(rt.BackupEndpoint),
		Model:           model,
		TimeoutMs:       runtimeTimeoutMs(rt.BackupTimeoutSec),
		Temperature:     rt.BackupTemperature,
		MaxTokens:       rt.BackupMaxTokens,
		ReasoningPreset: rt.BackupReasoningPreset,
		ReasoningEffort: rt.BackupReasoningEffort,
		ReasoningBudget: rt.BackupReasoningBudget,
	}
	// Left unset, the backup keeps the role's own temperature and timeout.
	if rt.BackupTemperatureFromRole {
		target.Temperature = nil
	}
	if rt.BackupTimeoutFromRole {
		target.TimeoutMs = 0
	}
	return target
}

// request keeps the primary call's messages and output limits and swaps in
// the backup's provider. Provider-specific overrides of the primary model do
// not carry over.
func (b *backupLLMTarget) request(primary dto.ProxyPluginMainRequest) dto.ProxyPluginMainRequest {
	provider, apiKey, endpoint, model := b.Provider, b.APIKey, b.Endpoint, b.Model
	out := dto.ProxyPluginMainRequest{
		APIKey:              &apiKey,
		Endpoint:            &endpoint,
		Model:               &model,
		Provider:            &provider,
		Messages:            primary.Messages,
		MaxTokens:           primary.MaxTokens,
		MaxCompletionTokens: primary.MaxCompletionTokens,
		Temperature:         primary.Temperature,
		TimeoutMs:           primary.TimeoutMs,
	}
	if b.MaxTokens != nil && *b.MaxTokens > 0 {
		maxTokens := *b.MaxTokens
		out.MaxTokens, out.MaxCompletionTokens = &maxTokens, &maxTokens
	}
	if b.Temperature != nil {
		temperature := *b.Temperature
		out.Temperature = &temperature
	}
	if b.TimeoutMs > 0 {
		timeoutMs := b.TimeoutMs
		out.TimeoutMs = &timeoutMs
	}
	if preset := strings.TrimSpace(b.ReasoningPreset); preset != "" {
		out.ReasoningPreset = &preset
	}
	if effort := strings.TrimSpace(b.ReasoningEffort); effort != "" {
		out.ReasoningEffort = &effort
	}
	if b.ReasoningBudget != nil && *b.ReasoningBudget > 0 {
		budget := *b.ReasoningBudget
		out.ReasoningBudgetTokens = &budget
	}
	return out
}

type backupLLMNoticeKey struct{}

// withBackupLLMNotice lets a turn show on its HUD that the backup answered.
func withBackupLLMNotice(ctx context.Context, notice func(trace map[string]any)) context.Context {
	if notice == nil {
		return ctx
	}
	return context.WithValue(ctx, backupLLMNoticeKey{}, notice)
}

func (s *Server) withBackupLLMWorkflowNotice(ctx context.Context, workflowRequestID string) context.Context {
	if s == nil || s.TurnWorkflows == nil || strings.TrimSpace(workflowRequestID) == "" {
		return ctx
	}
	return withBackupLLMNotice(ctx, func(trace map[string]any) {
		roles, _ := trace["roles"].([]string)
		reason, _ := trace["reason"].(string)
		s.TurnWorkflows.addBackupLLMWarning(workflowRequestID, roles, reason)
	})
}

// callProxyProviderWithBackup runs the primary call and, after a content
// failure, the backup once. The backup's answer carries a backup_llm trace in
// its response metadata; when the backup fails too the primary result stands.
func callProxyProviderWithBackup(ctx context.Context, req dto.ProxyPluginMainRequest, policy proxyRequestPolicy, retryBudget *llmRetryBudget) (map[string]any, int, error) {
	result, status, err := callProxyProviderOnce(ctx, req, policy, retryBudget)
	backup := policy.Backup
	if backup == nil || ctx.Err() != nil {
		return result, status, err
	}
	reason := proxyContentFailureReason(result, status, err, policy)
	if reason == "" {
		return result, status, err
	}
	backupPolicy := policy
	backupPolicy.Backup = nil
	backupResult, backupStatus, backupErr := callProxyProviderOnce(ctx, backup.request(req), backupPolicy, nil)
	trace := map[string]any{
		"used":             true,
		"roles":            backup.Roles,
		"reason":           reason,
		"primary_provider": strings.ToLower(strings.TrimSpace(stringPtrValue(req.Provider, ""))),
		"primary_model":    strings.TrimSpace(stringPtrValue(req.Model, "")),
		"backup_provider":  backup.Provider,
		"backup_model":     backup.Model,
	}
	if backupErr == nil {
		if backupReason := proxyContentFailureReason(backupResult, backupStatus, nil, policy); backupReason != "" {
			backupErr = errors.New("backup model also failed: " + backupReason)
		}
	}
	logArgs := []any{"purpose", policy.Purpose, "session_id", policy.SessionID, "roles", backup.Roles, "reason", reason,
		"primary_model", trace["primary_model"], "backup_model", backup.Model}
	if backupErr != nil {
		slog.WarnContext(ctx, "backup LLM failed too", append(logArgs, "error", scrubProxySecret(backupErr.Error(), backup.APIKey))...)
		trace["succeeded"] = false
		trace["error"] = scrubProxySecret(backupErr.Error(), backup.APIKey)
		if result == nil {
			result = map[string]any{}
		}
		attachBackupLLMTrace(result, trace)
		return result, status, err
	}
	slog.InfoContext(ctx, "backup LLM answered", logArgs...)
	trace["succeeded"] = true
	attachBackupLLMTrace(backupResult, trace)
	if notice, ok := ctx.Value(backupLLMNoticeKey{}).(func(map[string]any)); ok {
		notice(trace)
	}
	return backupResult, backupStatus, nil
}

func attachBackupLLMTrace(result map[string]any, trace map[string]any) {
	meta := mapFromAny(result[proxyResponseMetadataKey])
	if meta == nil {
		meta = map[string]any{}
	}
	meta["backup_llm"] = trace
	result[proxyResponseMetadataKey] = meta
}

// proxyContentFailureReason names why an answer counts as refused content,
// or returns "" for a usable answer or a failure the backup should not take.
func proxyContentFailureReason(result map[string]any, status int, err error, policy proxyRequestPolicy) string {
	meta := mapFromAny(result[proxyResponseMetadataKey])
	if stringFromMap(meta, "termination_kind") == "safety" {
		return "provider_safety:" + strings.ToLower(stringFromMap(meta, "native_finish_reason"))
	}
	if err != nil {
		var emptyErr *proxyEmptyContentError
		if errors.As(err, &emptyErr) {
			return "empty_response"
		}
		if (status == http.StatusBadRequest || status == http.StatusForbidden || status == http.StatusUnprocessableEntity) && proxyErrorLooksLikeContentPolicy(err.Error()) {
			return "provider_content_policy"
		}
		return ""
	}
	if stringFromMap(meta, "termination_kind") == "length" {
		return ""
	}
	text := strings.TrimSpace(chatCompletionText(result))
	if text == "" {
		return "empty_response"
	}
	_, jsonErr := parseJSONFromLLMContent(text)
	if jsonErr != nil && llmTextLooksLikeRefusal(text) {
		return "refusal_text"
	}
	if jsonErr != nil && (policy.JSONResponse || policy.ExpectJSON) {
		return "missing_json"
	}
	return ""
}

func proxyErrorLooksLikeContentPolicy(detail string) bool {
	lower := strings.ToLower(detail)
	for _, marker := range []string{"content_policy", "content policy", "content_filter", "content management policy", "prohibited_content", "safety", "responsible ai", "moderation"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

var llmRefusalMarkers = []string{
	"i cannot", "i can't", "i can’t", "i won't", "i won’t", "i'm sorry", "i’m sorry", "i am sorry",
	"i'm unable", "i’m unable", "i am unable", "i'm not able", "i’m not able", "i am not able",
	"cannot fulfill", "can't help with", "can’t help with", "cannot assist", "can't assist", "can’t assist",
	"against my guidelines", "content policy",
	"죄송", "할 수 없", "도와드릴 수 없", "제공할 수 없", "응할 수 없", "작성할 수 없",
	"申し訳", "できません", "お手伝いできません", "対応できません",
}

// llmTextLooksLikeRefusal only looks at short answers: a refusal is a few
// sentences, while real output that happens to apologize is longer.
func llmTextLooksLikeRefusal(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || len([]rune(text)) > 600 {
		return false
	}
	lower := strings.ToLower(text)
	for _, marker := range llmRefusalMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// backupLLMRoleKeys lists every role the settings can switch the backup on for.
func backupLLMRoleKeys() []string {
	keys := []string{
		backupRolePublisher, backupRoleCritic, backupRoleWorldRuleAudit,
		backupRoleChapter, backupRoleArc, backupRoleSaga,
		backupRoleSourceSearch, backupRoleReferenceLibrary, backupRoleMemoryImport,
	}
	for _, role := range []string{"event_recent", "character_objective", "subjective_relationship", "world_state", "unresolved_goal"} {
		keys = append(keys, backupRolePreprocessing+role)
	}
	sort.Strings(keys)
	return keys
}

// backupLLMRolesFromAny reads the role switches the plugin sends, either as a
// list of enabled roles or as an object of role to on/off. Unknown roles are
// dropped.
func backupLLMRolesFromAny(value any) map[string]bool {
	known := map[string]bool{}
	for _, role := range backupLLMRoleKeys() {
		known[role] = true
	}
	out := map[string]bool{}
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if role := strings.TrimSpace(extractionStringFromAny(item)); known[role] {
				out[role] = true
			}
		}
	case []string:
		for _, role := range typed {
			if role = strings.TrimSpace(role); known[role] {
				out[role] = true
			}
		}
	case map[string]any:
		for role, enabled := range typed {
			if known[role] && boolFromAny(enabled) {
				out[role] = true
			}
		}
	}
	return out
}

func backupPreprocessingRoles(roles []string) []string {
	out := make([]string, 0, len(roles))
	for _, role := range roles {
		out = append(out, backupRolePreprocessing+role)
	}
	return out
}

func (s *Server) llmConfigWithBackup(cfg completeTurnLLMConfig, roles ...string) completeTurnLLMConfig {
	cfg.Backup = s.backupLLMFor(roles...)
	return cfg
}
