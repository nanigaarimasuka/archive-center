package httpapi

import "strings"

// proxyForkTakeOpenRouterProvider moves an object-valued `provider` out of the
// extra body into the request body when the request goes to OpenRouter, where
// that field pins the upstream providers (order, only, allow_fallbacks). For
// every other provider `provider` stays a protected key.
func proxyForkTakeOpenRouterProvider(body, extraBody map[string]any, provider string, trace map[string]any) bool {
	if !strings.EqualFold(strings.TrimSpace(provider), "openrouter") {
		return false
	}
	routing, ok := extraBody["provider"].(map[string]any)
	if !ok || len(routing) == 0 {
		return false
	}
	delete(extraBody, "provider")
	body["provider"] = routing
	trace["openrouter_provider_routing"] = routing
	return true
}
