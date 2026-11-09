package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// The backup keeps its own OpenRouter provider pin and sends it as
// provider.order with no fallbacks; other backup providers send nothing.
func TestBackupLLMSendsItsOwnOpenRouterProviderRoute(t *testing.T) {
	for _, tc := range []struct {
		provider, endpoint string
		want               any
	}{
		{"openrouter", "https://openrouter.ai/api/v1", map[string]any{"order": []any{"google-vertex", "deepinfra"}, "allow_fallbacks": false}},
		{"openai", "https://backup.example.com/v1", nil},
	} {
		var mu sync.Mutex
		var backupBody map[string]any
		oldClient := proxyHTTPClient
		proxyHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			reply := chatReply("I cannot fulfill this request.", "stop")
			if extractionStringFromAny(body["model"]) == "backup-model" {
				mu.Lock()
				backupBody = body
				mu.Unlock()
				reply = chatReply(`{"ok":true}`, "stop")
			}
			status, text := reply()
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(text))}, nil
		})}
		srv := backupTestServer(backupRoleCritic)
		srv.updateRuntimeConfig(map[string]any{
			"backupProvider":                tc.provider,
			"backupEndpoint":                tc.endpoint,
			"backupOpenRouterProviderRoute": " google-vertex, deepinfra ",
		})
		_, _, err := performProxyPluginMainWithRetryBudgetAndPolicy(context.Background(), backupTestRequest(), nil, proxyRequestPolicy{ExpectJSON: true, Backup: srv.backupLLMFor(backupRoleCritic)})
		proxyHTTPClient = oldClient
		if err != nil {
			t.Fatalf("%s: %v", tc.provider, err)
		}
		if backupBody == nil {
			t.Fatalf("%s: backup was not called", tc.provider)
		}
		if got := backupBody["provider"]; !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: provider = %#v, want %#v", tc.provider, got, tc.want)
		}
	}
	if backupForkOpenRouterExtraBody("openrouter", " , ") != nil {
		t.Fatal("an empty route must send nothing")
	}
}
