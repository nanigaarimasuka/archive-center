package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/risulongmemory/archive-center-go/internal/config"
	"github.com/risulongmemory/archive-center-go/internal/dto"
)

// backupTestProvider answers each model with a canned OpenAI-style reply and
// records which models were called.
type backupTestProvider struct {
	mu      sync.Mutex
	called  []string
	replies map[string]func() (int, string)
}

func (p *backupTestProvider) install(t *testing.T) {
	t.Helper()
	oldClient := proxyHTTPClient
	proxyHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		model := extractionStringFromAny(body["model"])
		p.mu.Lock()
		p.called = append(p.called, model)
		p.mu.Unlock()
		status, text := http.StatusInternalServerError, `{"error":{"message":"no reply"}}`
		if reply := p.replies[model]; reply != nil {
			status, text = reply()
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(text))}, nil
	})}
	t.Cleanup(func() { proxyHTTPClient = oldClient })
}

func chatReply(content, finishReason string) func() (int, string) {
	return func() (int, string) {
		raw, _ := json.Marshal(map[string]any{
			"model":   "x",
			"choices": []any{map[string]any{"message": map[string]any{"content": content}, "finish_reason": finishReason}},
		})
		return http.StatusOK, string(raw)
	}
}

func backupTestServer(roles ...string) *Server {
	srv := NewServer(config.Default())
	srv.updateRuntimeConfig(map[string]any{
		"backupLlmEnabled": true,
		"backupProvider":   "openai",
		"backupApiKey":     "sk-backup",
		"backupEndpoint":   "https://backup.example.com/v1",
		"backupModel":      "backup-model",
		"backupLlmRoles":   toAnySlice(roles),
	})
	return srv
}

func toAnySlice(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func backupTestRequest() dto.ProxyPluginMainRequest {
	provider, key, endpoint, model := "openai", "sk-primary", "https://primary.example.com/v1", "primary-model"
	timeout := int64(5000)
	return dto.ProxyPluginMainRequest{
		Provider: &provider, APIKey: &key, Endpoint: &endpoint, Model: &model, TimeoutMs: &timeout,
		Messages: []any{map[string]any{"role": "user", "content": "summarize"}},
	}
}

func TestBackupLLMAnswersARefusalOnce(t *testing.T) {
	provider := &backupTestProvider{replies: map[string]func() (int, string){
		"primary-model": chatReply("I cannot fulfill this request.", "stop"),
		"backup-model":  chatReply(`{"summary_text":"ok"}`, "stop"),
	}}
	provider.install(t)
	srv := backupTestServer(backupRoleChapter)
	noticed := 0
	ctx := withBackupLLMNotice(context.Background(), func(map[string]any) { noticed++ })

	result, _, err := performProxyPluginMainWithRetryBudgetAndPolicy(ctx, backupTestRequest(), nil, proxyRequestPolicy{ExpectJSON: true, Backup: srv.backupLLMFor(backupRoleChapter)})
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if got := chatCompletionText(result); got != `{"summary_text":"ok"}` {
		t.Fatalf("answer = %q, want the backup's", got)
	}
	trace := mapFromAny(mapFromAny(result[proxyResponseMetadataKey])["backup_llm"])
	if trace["reason"] != "refusal_text" || trace["succeeded"] != true || trace["primary_model"] != "primary-model" {
		t.Fatalf("backup trace = %+v", trace)
	}
	if strings.Join(provider.called, ",") != "primary-model,backup-model" || noticed != 1 {
		t.Fatalf("calls = %v, notices = %d", provider.called, noticed)
	}
}

func TestBackupLLMTakesSafetyStopsAndProseForJSON(t *testing.T) {
	cases := map[string]struct {
		reply  func() (int, string)
		reason string
	}{
		"content filter": {chatReply("", "content_filter"), "provider_safety:content_filter"},
		"prose for json": {chatReply("Here is a long story about the chapter without any structure at all, which is not JSON.", "stop"), "missing_json"},
		"empty":          {chatReply("", "stop"), "empty_response"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			provider := &backupTestProvider{replies: map[string]func() (int, string){
				"primary-model": tc.reply,
				"backup-model":  chatReply(`{"ok":true}`, "stop"),
			}}
			provider.install(t)
			srv := backupTestServer(backupRoleCritic)
			result, _, err := performProxyPluginMainWithRetryBudgetAndPolicy(context.Background(), backupTestRequest(), nil, proxyRequestPolicy{ExpectJSON: true, Backup: srv.backupLLMFor(backupRoleCritic)})
			if err != nil {
				t.Fatalf("call failed: %v", err)
			}
			if reason := mapFromAny(mapFromAny(result[proxyResponseMetadataKey])["backup_llm"])["reason"]; reason != tc.reason {
				t.Fatalf("reason = %v, want %s", reason, tc.reason)
			}
		})
	}
}

func TestBackupLLMLeavesTransportFailuresAndDisabledRolesAlone(t *testing.T) {
	provider := &backupTestProvider{replies: map[string]func() (int, string){
		"primary-model": func() (int, string) { return http.StatusServiceUnavailable, `{"error":{"message":"overloaded"}}` },
		"backup-model":  chatReply(`{"ok":true}`, "stop"),
	}}
	provider.install(t)
	srv := backupTestServer(backupRoleChapter)
	if _, _, err := performProxyPluginMainWithRetryBudgetAndPolicy(context.Background(), backupTestRequest(), nil, proxyRequestPolicy{ExpectJSON: true, Backup: srv.backupLLMFor(backupRoleChapter)}); err == nil {
		t.Fatal("a 503 was answered by the backup")
	}
	for _, model := range provider.called {
		if model == "backup-model" {
			t.Fatalf("backup called for a transport failure: %v", provider.called)
		}
	}
	if srv.backupLLMFor(backupRoleCritic) != nil {
		t.Fatal("the backup applies to a role that was not switched on")
	}
	if srv.backupLLMFor(backupRolePreprocessing+"world_state", backupRoleChapter) == nil {
		t.Fatal("a shared call should use the backup when one of its roles has it on")
	}
}

func TestBackupLLMFailureKeepsThePrimaryResult(t *testing.T) {
	provider := &backupTestProvider{replies: map[string]func() (int, string){
		"primary-model": chatReply("I'm sorry, but I can't help with that.", "stop"),
		"backup-model":  chatReply("I'm sorry, I can't either.", "stop"),
	}}
	provider.install(t)
	srv := backupTestServer(backupRolePublisher)
	result, _, err := performProxyPluginMainWithRetryBudgetAndPolicy(context.Background(), backupTestRequest(), nil, proxyRequestPolicy{Backup: srv.backupLLMFor(backupRolePublisher)})
	if err != nil {
		t.Fatalf("primary result should stand without a new error: %v", err)
	}
	if got := chatCompletionText(result); !strings.Contains(got, "can't help") {
		t.Fatalf("answer = %q, want the primary's", got)
	}
	trace := mapFromAny(mapFromAny(result[proxyResponseMetadataKey])["backup_llm"])
	if trace["succeeded"] != false {
		t.Fatalf("trace = %+v, want a failed backup", trace)
	}
}

func TestLLMTextLooksLikeRefusal(t *testing.T) {
	for _, text := range []string{"I cannot fulfill this request.", "죄송하지만 이 요청은 도와드릴 수 없습니다.", "申し訳ありませんが、対応できません。"} {
		if !llmTextLooksLikeRefusal(text) {
			t.Fatalf("refusal not recognized: %q", text)
		}
	}
	long := strings.Repeat("이야기가 이어진다. ", 100) + "죄송합니다."
	if llmTextLooksLikeRefusal(long) {
		t.Fatal("a long answer that apologizes was taken for a refusal")
	}
}

func TestBackupLLMCanKeepTheRoleTemperatureAndTimeout(t *testing.T) {
	srv := backupTestServer(backupRoleChapter)
	srv.updateRuntimeConfig(map[string]any{"backupTemperature": 0.0, "backupTimeout": 30})
	req := backupTestRequest()
	roleTemperature := 0.9
	req.Temperature = &roleTemperature

	own := srv.backupLLMFor(backupRoleChapter).request(req)
	if *own.Temperature != 0 || *own.TimeoutMs != 30000 {
		t.Fatalf("own settings: temperature=%v timeout=%v, want 0 and 30000", *own.Temperature, *own.TimeoutMs)
	}
	srv.updateRuntimeConfig(map[string]any{"backupTemperatureFromRole": true, "backupTimeoutFromRole": true})
	inherited := srv.backupLLMFor(backupRoleChapter).request(req)
	if *inherited.Temperature != 0.9 || *inherited.TimeoutMs != 5000 {
		t.Fatalf("role settings: temperature=%v timeout=%v, want the role's 0.9 and 5000", *inherited.Temperature, *inherited.TimeoutMs)
	}
}
