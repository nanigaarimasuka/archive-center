package httpapi

import (
	"reflect"
	"testing"

	"github.com/risulongmemory/archive-center-go/internal/dto"
)

func TestOpenRouterProviderRoutingPassesThroughExtraBody(t *testing.T) {
	extra := `{"provider":{"order":["google-vertex"],"allow_fallbacks":false},"top_k":5}`
	cases := []struct {
		provider string
		pinned   bool
	}{
		{"openrouter", true},
		{"OpenRouter", true},
		{"openai", false},
		{"llmgateway", false},
	}
	for _, tc := range cases {
		body := map[string]any{"model": "deepseek/deepseek-v4.1-flash"}
		raw := extra
		trace, err := proxyApplyRequestOverrides(map[string]string{}, body, dto.ProxyPluginMainRequest{ExtraBodyJSON: &raw}, tc.provider, false)
		if err != nil {
			t.Fatalf("%s: %v", tc.provider, err)
		}
		if body["top_k"] == nil {
			t.Fatalf("%s: other extra body keys must still apply: %v", tc.provider, body)
		}
		routing, ok := body["provider"].(map[string]any)
		if ok != tc.pinned {
			t.Fatalf("%s: provider routing applied=%v, want %v (trace %v)", tc.provider, ok, tc.pinned, trace)
		}
		if !tc.pinned {
			if !reflect.DeepEqual(trace["extra_body_blocked"], []string{"provider"}) {
				t.Fatalf("%s: provider should stay blocked: %v", tc.provider, trace)
			}
			continue
		}
		if !reflect.DeepEqual(routing["order"], []any{"google-vertex"}) || routing["allow_fallbacks"] != false {
			t.Fatalf("routing = %v", routing)
		}
		if trace["openrouter_provider_routing"] == nil {
			t.Fatalf("trace = %v", trace)
		}
	}
	// A string provider is never taken, even for OpenRouter.
	body := map[string]any{}
	raw := `{"provider":"anthropic"}`
	trace, err := proxyApplyRequestOverrides(map[string]string{}, body, dto.ProxyPluginMainRequest{ExtraBodyJSON: &raw}, "openrouter", false)
	if err != nil || body["provider"] != nil || !reflect.DeepEqual(trace["extra_body_blocked"], []string{"provider"}) {
		t.Fatalf("body=%v trace=%v err=%v", body, trace, err)
	}
}
