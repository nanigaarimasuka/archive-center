package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/risulongmemory/archive-center-go/internal/config"
	"github.com/risulongmemory/archive-center-go/internal/dto"
	"github.com/risulongmemory/archive-center-go/internal/vector"
)

// recallCompletionOrderVector answers concurrent searches after a delay chosen
// per (session, query, filter), so completion order differs between runs.
type recallCompletionOrderVector struct {
	vector.VectorStore
	delay func(sessionIndex, queryIndex int, memory bool) time.Duration

	mu    sync.Mutex
	calls []string
}

func (v *recallCompletionOrderVector) Health(context.Context) (vector.HealthSnapshot, error) {
	return vector.HealthSnapshot{Status: "ok", ModelReady: true}, nil
}

func (v *recallCompletionOrderVector) Search(_ context.Context, sid string, q []float32, _ int, filter string) ([]vector.VectorDocument, error) {
	sessionIndex := map[string]int{"root": 0, "branch": 1}[sid]
	queryIndex := int(q[0]) - 1
	memory := filter == `tier == "memory"`
	v.mu.Lock()
	v.calls = append(v.calls, fmt.Sprintf("%s/%d/%t", sid, queryIndex, memory))
	v.mu.Unlock()
	time.Sleep(v.delay(sessionIndex, queryIndex, memory))
	if sid == "branch" && queryIndex == 2 && memory {
		return nil, fmt.Errorf("fixture search failure")
	}
	// Shared IDs across queries, with ranks and similarities that differ per
	// query, exercise duplicate ownership and observation order.
	docs := []vector.VectorDocument{}
	for rank := 0; rank < 3; rank++ {
		id := []string{"a", "b", "c"}[(rank+queryIndex)%3]
		similarity := 0.9 - 0.1*float64(rank) + 0.01*float64(queryIndex)
		docs = append(docs, vector.VectorDocument{
			ID: "memory:" + sid + ":" + id, ChatSessionID: sid, Tier: "memory", SourceTable: "memories", SourceRowID: id,
			DocumentText: id + " text", Similarity: similarity, SimilarityAvailable: true, SimilaritySource: "cosine",
		})
	}
	return docs, nil
}

func TestPrepareTurnVectorRecallMergeIgnoresCompletionOrder(t *testing.T) {
	const current = "resume exercise"
	oldClient := proxyHTTPClient
	proxyHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		value := map[string]int{current: 1, "user:\nfirst\nassistant:\nfirst reply": 2, "user:\nsecond\nassistant:\nsecond reply": 3}[extractionStringFromAny(body["input"])]
		if value == 0 {
			t.Errorf("unexpected input %q", body["input"])
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"data":[{"embedding":[%d,0.2,0.3]}]}`, value)))}, nil
	})}
	defer func() { proxyHTTPClient = oldClient }()
	scope := prepareTurnHistoryScope{
		State:  "ready",
		Reason: "confirmed_worldline_history_composed",
		Segments: []prepareTurnHistorySegment{
			{SessionID: "root", FromTurn: 0, ToTurn: 8},
			{SessionID: "branch", FromTurn: 9, ToTurn: 9},
		},
	}
	run := func(delay func(sessionIndex, queryIndex int, memory bool) time.Duration) (map[string]any, []string) {
		cfg := config.Default()
		cfg.ChromaEndpoint = "http://offline-index.invalid"
		srv := NewServer(cfg)
		vec := &recallCompletionOrderVector{delay: delay}
		srv.Vector = vec
		srv.VectorOpenError = nil
		currentInput := current
		req := dto.PrepareTurnRequest{ChatSessionID: "branch", RawUserInput: &currentInput,
			Messages:   []map[string]any{{"role": "user", "content": "second"}, {"role": "assistant", "content": "second reply"}, {"role": "user", "content": "first"}, {"role": "assistant", "content": "first reply"}},
			ClientMeta: map[string]any{"embedding": map[string]any{"provider": "custom", "endpoint": "https://offline.example.test/v1", "api_key": "fixture", "model": "fixture", "timeout_ms": 30000}}}
		req.Settings.ApplyDefaults()
		two := 2
		req.Settings.RecentConversationReferenceCount = &two
		shadow := srv.prepareTurnVectorShadow(context.Background(), req, 5, scope)
		if intFromAny(shadow["query_vector_count"], 0) != 3 {
			t.Fatalf("fixture did not produce three query vectors: %v", shadow["query_vector_count"])
		}
		return shadow, vec.calls
	}
	// Earlier calls finish first, like serial execution; then the reverse.
	forward, forwardCalls := run(func(s, q int, memory bool) time.Duration {
		order := s*3 + q
		if memory {
			order += 6
		}
		return time.Duration(order) * 3 * time.Millisecond
	})
	reverse, reverseCalls := run(func(s, q int, memory bool) time.Duration {
		order := s*3 + q
		if memory {
			order += 6
		}
		return time.Duration(12-order) * 3 * time.Millisecond
	})
	if len(forwardCalls) != 12 || len(reverseCalls) != 12 {
		t.Fatalf("every session, query and pass must be searched once: %v / %v", forwardCalls, reverseCalls)
	}
	for _, key := range []string{"search_result", "search_results", "memory_search_result", "memory_search_results", "query_observations", "history_session_ids", "status"} {
		left, _ := json.Marshal(forward[key])
		right, _ := json.Marshal(reverse[key])
		if string(left) != string(right) {
			t.Fatalf("%s depends on completion order:\nforward=%s\nreverse=%s", key, left, right)
		}
	}
	// Observations keep serial query order inside each merged hit.
	for _, hit := range prepareTurnVectorMemorySearchResultMaps(reverse) {
		indexes := []int{}
		for _, observation := range sliceFromAny(hit["recall_queries"]) {
			indexes = append(indexes, intFromAny(mapFromAny(observation)["query_index"], -1))
		}
		want := []int{0, 1, 2}
		if strings.HasPrefix(extractionStringFromAny(hit["id"]), "memory:branch:") {
			want = []int{0, 1}
		}
		if !reflect.DeepEqual(indexes, want) {
			t.Fatalf("recall query order for %v = %v, want %v", hit["id"], indexes, want)
		}
	}
	if counts, _ := reverse["query_observations"].(map[string]int); counts["memory.failures"] != 1 {
		t.Fatalf("failed search was not counted: %v", reverse["query_observations"])
	}
}
