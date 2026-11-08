package main

import (
	"strings"
	"testing"
)

func TestEmbeddingConcurrencyAndBatchSizeAreSyncedSettings(t *testing.T) {
	src := readArchiveCenterJS(t)
	for _, want := range []string{
		`embeddingConcurrency: 8,`,
		`embeddingBatchSize: 16,`,
		`merged.embeddingConcurrency = Math.trunc(sanitizeNumber(merged.embeddingConcurrency, DEFAULT_SETTINGS.embeddingConcurrency, 1, 32));`,
		`merged.embeddingBatchSize = Math.trunc(sanitizeNumber(merged.embeddingBatchSize, DEFAULT_SETTINGS.embeddingBatchSize, 1, 128));`,
		`embeddingConcurrency: Math.trunc(sanitizeNumber(s.embeddingConcurrency, DEFAULT_SETTINGS.embeddingConcurrency, 1, 32)),`,
		`embeddingBatchSize: Math.trunc(sanitizeNumber(s.embeddingBatchSize, DEFAULT_SETTINGS.embeddingBatchSize, 1, 128)),`,
		`id="mo-embeddingConcurrency"`,
		`id="mo-embeddingBatchSize"`,
		`embeddingConcurrency: $("mo-embeddingConcurrency") ? $("mo-embeddingConcurrency").value : settings.embeddingConcurrency,`,
		`embeddingBatchSize: $("mo-embeddingBatchSize") ? $("mo-embeddingBatchSize").value : settings.embeddingBatchSize,`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("missing from the plugin: %s", want)
		}
	}
	for _, key := range []string{"settings.label.embeddingConcurrency", "settings.label.embeddingBatchSize.hint"} {
		if strings.Count(src, `"`+key+`":`) != 3 {
			t.Fatalf("%s is not in all three languages", key)
		}
	}
}
