package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/risulongmemory/archive-center-go/internal/dto"
)

// The "risu" provider ("RisuAI plugin models") runs LLM calls in the RisuAI
// tab, through the plugin's runLLMModel, with a model another plugin added to
// RisuAI (e.g. PageFold). The backend queues a call; the plugin takes it by
// long poll, runs it and posts the result. Without a connected tab, calls
// fail.

const (
	risuProvider = "risu"
	// risuBridgeEndpoint stands in for an endpoint: Risu calls have none.
	risuBridgeEndpoint = "risu://plugin"
	// A tab polls again right after each poll; one that has not polled for
	// this long is not connected.
	risuBridgePollWait        = 20 * time.Second
	risuBridgeConnectedWithin = 45 * time.Second
	// Calls without their own time limit end after this.
	risuBridgeDefaultTimeout = 10 * time.Minute
)

// risuBridgeCall is what the plugin passes to runLLMModel.
type risuBridgeCall struct {
	ID                   string           `json:"id"`
	Model                string           `json:"model"`
	Messages             []map[string]any `json:"messages"`
	MaxTokens            *int64           `json:"max_tokens,omitempty"`
	Temperature          *float64         `json:"temperature,omitempty"`
	ReasoningEffort      *int             `json:"reasoning_effort,omitempty"` // Risu's scale: -1 off .. 3 extra high
	ThinkingTokens       *int64           `json:"thinking_tokens,omitempty"`
	AdditionalParameters [][2]string      `json:"additional_parameters,omitempty"`
	TimeoutMs            int64            `json:"timeout_ms,omitempty"`
	done                 chan risuBridgeResult
}

type risuBridgeResult struct {
	content string
	err     string
}

type risuBridge struct {
	mu       sync.Mutex
	queue    []*risuBridgeCall
	pending  map[string]*risuBridgeCall
	wake     chan struct{} // closed when a call is queued
	polled   chan struct{} // closed when a tab polls
	lastPoll time.Time
	now      func() time.Time
}

func newRisuBridge() *risuBridge {
	return &risuBridge{pending: map[string]*risuBridgeCall{}, wake: make(chan struct{}), polled: make(chan struct{}), now: time.Now}
}

// A tab that reloads polls again within moments; a call waits this long for one.
const risuBridgeConnectGrace = 5 * time.Second

var sharedRisuBridge = newRisuBridge()

// call queues c for the plugin and waits for its result.
func (b *risuBridge) call(ctx context.Context, c *risuBridgeCall) (string, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	c.ID, c.done = hex.EncodeToString(id), make(chan risuBridgeResult, 1)
	b.mu.Lock()
	if b.lastPoll.IsZero() || b.now().Sub(b.lastPoll) > risuBridgeConnectedWithin {
		polled := b.polled
		b.mu.Unlock()
		grace := time.NewTimer(risuBridgeConnectGrace)
		defer grace.Stop()
		select {
		case <-polled:
		case <-grace.C:
			return "", errors.New("no RisuAI tab with Archive Center is connected to run the Risu model")
		case <-ctx.Done():
			return "", fmt.Errorf("risu model call ended before a RisuAI tab connected: %w", ctx.Err())
		}
		b.mu.Lock()
	}
	b.queue = append(b.queue, c)
	b.pending[c.ID] = c
	close(b.wake)
	b.wake = make(chan struct{})
	b.mu.Unlock()
	select {
	case result := <-c.done:
		if result.err != "" {
			return "", fmt.Errorf("risu model call failed: %s", result.err)
		}
		return result.content, nil
	case <-ctx.Done():
		b.mu.Lock()
		delete(b.pending, c.ID)
		for i, queued := range b.queue {
			if queued == c {
				b.queue = append(b.queue[:i], b.queue[i+1:]...)
				break
			}
		}
		b.mu.Unlock()
		return "", fmt.Errorf("risu model call ended before RisuAI answered: %w", ctx.Err())
	}
}

// take returns the next queued call, waiting up to wait for one.
func (b *risuBridge) take(ctx context.Context, wait time.Duration) *risuBridgeCall {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		b.mu.Lock()
		b.lastPoll = b.now()
		close(b.polled)
		b.polled = make(chan struct{})
		if len(b.queue) > 0 {
			c := b.queue[0]
			b.queue = b.queue[1:]
			b.mu.Unlock()
			return c
		}
		wake := b.wake
		b.mu.Unlock()
		select {
		case <-wake:
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

// complete delivers a call's result; it reports whether the call was waiting.
func (b *risuBridge) complete(id string, result risuBridgeResult) bool {
	b.mu.Lock()
	c := b.pending[id]
	delete(b.pending, id)
	b.mu.Unlock()
	if c == nil {
		return false
	}
	c.done <- result
	return true
}

func (s *Server) registerRisuBridgeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /risu-bridge/next", s.handleRisuBridgeNext)
	mux.HandleFunc("POST /risu-bridge/result", s.handleRisuBridgeResult)
}

func (s *Server) handleRisuBridgeNext(w http.ResponseWriter, r *http.Request) {
	// call is null when none was queued within the wait.
	writeJSON(w, http.StatusOK, map[string]any{"call": sharedRisuBridge.take(r.Context(), risuBridgePollWait)})
}

func (s *Server) handleRisuBridgeResult(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID      string `json:"id"`
		OK      bool   `json:"ok"`
		Content string `json:"content"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.ID) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "id is required")
		return
	}
	result := risuBridgeResult{content: body.Content}
	if !body.OK {
		result.err = strings.TrimSpace(body.Error)
		if result.err == "" {
			result.err = "RisuAI returned no result"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": sharedRisuBridge.complete(body.ID, result)})
}

// proxyCallRisu runs req through the RisuAI tab, with req's generation
// settings in place of Risu's.
func proxyCallRisu(ctx context.Context, req dto.ProxyPluginMainRequest, model string) (map[string]any, int, error) {
	c, err := risuBridgeCallFor(req, model)
	if err != nil {
		return nil, http.StatusBadRequest, &proxyLocalRequestError{Stage: "configuration", Cause: err}
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, risuBridgeDefaultTimeout)
		defer cancel()
	}
	if deadline, ok := ctx.Deadline(); ok && c.TimeoutMs <= 0 {
		c.TimeoutMs = time.Until(deadline).Milliseconds()
	}
	content, err := sharedRisuBridge.call(ctx, c)
	if err != nil {
		return nil, http.StatusBadGateway, err
	}
	if strings.TrimSpace(content) == "" {
		return nil, http.StatusBadGateway, &proxyEmptyContentError{Provider: risuProvider}
	}
	return proxyNormalizeChatResponse(content, model, "stop"), http.StatusOK, nil
}

func risuBridgeCallFor(req dto.ProxyPluginMainRequest, model string) (*risuBridgeCall, error) {
	c := &risuBridgeCall{Model: model, Temperature: req.Temperature}
	for _, raw := range req.Messages {
		message := mapFromAny(raw)
		role := strings.ToLower(strings.TrimSpace(extractionStringFromAny(message["role"])))
		if role != "system" && role != "assistant" {
			role = "user"
		}
		c.Messages = append(c.Messages, map[string]any{"role": role, "content": risuBridgeMessageText(message["content"])})
	}
	if len(c.Messages) == 0 {
		return nil, errors.New("messages are required")
	}
	switch {
	case req.MaxCompletionTokens != nil && *req.MaxCompletionTokens > 0:
		c.MaxTokens = req.MaxCompletionTokens
	case req.MaxTokens != nil && *req.MaxTokens > 0:
		c.MaxTokens = req.MaxTokens
	}
	if effort, ok := risuReasoningEffort(stringPtrValue(req.ReasoningEffort, "")); ok {
		c.ReasoningEffort = &effort
	}
	switch {
	case req.ReasoningBudgetTokens != nil && *req.ReasoningBudgetTokens > 0:
		c.ThinkingTokens = req.ReasoningBudgetTokens
	case req.BudgetTokens != nil && *req.BudgetTokens > 0:
		c.ThinkingTokens = req.BudgetTokens
	}
	if req.TimeoutMs != nil && *req.TimeoutMs > 0 {
		c.TimeoutMs = *req.TimeoutMs
	}
	// Extra body fields and headers become Risu additional parameters:
	// "json::" values are parsed as JSON and "header::" keys set headers.
	if text := strings.TrimSpace(stringPtrValue(req.ExtraBodyJSON, "")); text != "" {
		var body map[string]any
		if err := json.Unmarshal([]byte(text), &body); err != nil {
			return nil, fmt.Errorf("extra_body_json must be a JSON object: %w", err)
		}
		for _, key := range sortedMapKeys(body) {
			value, _ := json.Marshal(body[key])
			c.AdditionalParameters = append(c.AdditionalParameters, [2]string{key, "json::" + string(value)})
		}
	}
	if text := strings.TrimSpace(stringPtrValue(req.ExtraHeadersJSON, "")); text != "" {
		var headers map[string]any
		if err := json.Unmarshal([]byte(text), &headers); err != nil {
			return nil, fmt.Errorf("extra_headers_json must be a JSON object: %w", err)
		}
		for _, key := range sortedMapKeys(headers) {
			c.AdditionalParameters = append(c.AdditionalParameters, [2]string{"header::" + key, fmt.Sprint(headers[key])})
		}
	}
	return c, nil
}

// risuBridgeMessageText is a message's text; content parts are joined.
func risuBridgeMessageText(content any) string {
	if text, ok := content.(string); ok {
		return text
	}
	parts := []string{}
	for _, raw := range sliceFromAny(content) {
		part := mapFromAny(raw)
		if text := extractionStringFromAny(part["text"]); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

// risuReasoningEffort maps a reasoning effort to Risu's scale.
func risuReasoningEffort(effort string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none", "minimal", "off", "disable", "disabled", "false":
		return -1, true
	case "low":
		return 0, true
	case "medium":
		return 1, true
	case "high", "enable", "enabled", "true":
		return 2, true
	case "xhigh", "max":
		return 3, true
	}
	return 0, false
}

// llmProviderNeedsAPIKey reports whether calls to provider need an API key.
func llmProviderNeedsAPIKey(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	return provider != "ollama" && provider != risuProvider
}
