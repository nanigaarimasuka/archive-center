package httpapi

import (
	"encoding/json"
	"strings"
)

// backupForkOpenRouterExtraBody pins the backup's OpenRouter upstream
// providers ("a, b" → provider.order with no fallbacks). The backup does not
// inherit the primary model's overrides, so it carries its own; other
// providers get nothing.
func backupForkOpenRouterExtraBody(provider, route string) *string {
	if !strings.EqualFold(strings.TrimSpace(provider), "openrouter") {
		return nil
	}
	slugs := []string{}
	for _, slug := range strings.Split(route, ",") {
		if slug = strings.TrimSpace(slug); slug != "" {
			slugs = append(slugs, slug)
		}
	}
	if len(slugs) == 0 {
		return nil
	}
	raw, err := json.Marshal(map[string]any{"provider": map[string]any{"order": slugs, "allow_fallbacks": false}})
	if err != nil {
		return nil
	}
	text := string(raw)
	return &text
}
