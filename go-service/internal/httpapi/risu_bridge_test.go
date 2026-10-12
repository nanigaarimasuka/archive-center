package httpapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/risulongmemory/archive-center-go/internal/dto"
)

func TestRisuBridgeRunsCallsThroughAConnectedTab(t *testing.T) {
	b := newRisuBridge()
	if _, err := b.call(context.Background(), &risuBridgeCall{Model: "m"}); err == nil || !strings.Contains(err.Error(), "connected") {
		t.Fatalf("call without a tab: %v", err)
	}
	// A tab polls; a call queued meanwhile is handed to it.
	taken := make(chan *risuBridgeCall, 1)
	go func() { taken <- b.take(context.Background(), time.Second) }()
	for {
		b.mu.Lock()
		polled := !b.lastPoll.IsZero()
		b.mu.Unlock()
		if polled {
			break
		}
		time.Sleep(time.Millisecond)
	}
	result := make(chan string, 1)
	go func() {
		content, err := b.call(context.Background(), &risuBridgeCall{Model: "m"})
		if err != nil {
			content = "error: " + err.Error()
		}
		result <- content
	}()
	c := <-taken
	if c == nil || c.Model != "m" {
		t.Fatalf("taken call %+v", c)
	}
	if !b.complete(c.ID, risuBridgeResult{content: "answer"}) {
		t.Fatal("result not accepted")
	}
	if got := <-result; got != "answer" {
		t.Fatalf("call returned %q", got)
	}
	if b.complete(c.ID, risuBridgeResult{content: "again"}) {
		t.Fatal("a second result was accepted")
	}
	// A call whose time runs out leaves the queue.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := b.call(ctx, &risuBridgeCall{Model: "m"}); err == nil {
		t.Fatal("expired call succeeded")
	}
	b.mu.Lock()
	queued, pending := len(b.queue), len(b.pending)
	b.mu.Unlock()
	if queued != 0 || pending != 0 {
		t.Fatalf("expired call left queued=%d pending=%d", queued, pending)
	}
}

func TestRisuBridgeCallCarriesGenerationSettings(t *testing.T) {
	max, temperature, budget, timeout := int64(4096), 0.3, int64(2048), int64(90000)
	effort, body, headers := "high", `{"top_p":0.9,"response_format":{"type":"json_object"}}`, `{"X-Trace":"on"}`
	c, err := risuBridgeCallFor(dto.ProxyPluginMainRequest{
		Messages: []any{
			map[string]any{"role": "system", "content": "rules"},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "a"}, map[string]any{"type": "text", "text": "b"}}},
			map[string]any{"role": "tool", "content": "t"},
		},
		MaxCompletionTokens: &max, Temperature: &temperature, ReasoningEffort: &effort,
		ReasoningBudgetTokens: &budget, TimeoutMs: &timeout, ExtraBodyJSON: &body, ExtraHeadersJSON: &headers,
	}, "pluginmodel:::PageFold")
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != "pluginmodel:::PageFold" || len(c.Messages) != 3 || c.Messages[1]["content"] != "a\nb" || c.Messages[2]["role"] != "user" {
		t.Fatalf("messages %+v", c.Messages)
	}
	if *c.MaxTokens != 4096 || *c.Temperature != 0.3 || *c.ReasoningEffort != 2 || *c.ThinkingTokens != 2048 || c.TimeoutMs != 90000 {
		t.Fatalf("settings %+v", c)
	}
	want := [][2]string{{"response_format", `json::{"type":"json_object"}`}, {"top_p", "json::0.9"}, {"header::X-Trace", "on"}}
	if len(c.AdditionalParameters) != len(want) {
		t.Fatalf("additional parameters %v", c.AdditionalParameters)
	}
	for i := range want {
		if c.AdditionalParameters[i] != want[i] {
			t.Fatalf("additional parameters %v, want %v", c.AdditionalParameters, want)
		}
	}
}
