package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/risulongmemory/archive-center-go/internal/store"
)

// timelinePageView is what the plugin draws a /timeline page from: the
// items with their display fields and the turn groups they fall into.
// Pages end on turn boundaries, so a page's groups are whole and the
// plugin appends the next page's groups after them.
type timelinePageView struct {
	Items       []map[string]any            `json:"items"`
	Groups      []presentationTimelineGroup `json:"groups"`
	CurrentTurn int                         `json:"current_turn"`
}

type presentationTimelineGroup struct {
	Key         string           `json:"key"`
	TurnText    string           `json:"turn_text"`
	CreatedText string           `json:"created_text"`
	Kind        string           `json:"kind"`
	Preview     string           `json:"preview"`
	Counts      map[string]int   `json:"counts"`
	ItemKeys    []string         `json:"item_keys"`
	ItemCount   int              `json:"item_count"`
	items       []map[string]any // for the preview; the plugin finds items by key
}

func buildTimelinePageView(source []map[string]any) timelinePageView {
	view := timelinePageView{Items: make([]map[string]any, 0, len(source)), Groups: []presentationTimelineGroup{}}
	groupIndex := map[string]int{}
	for _, raw := range source {
		item := presentationTimelineItem(raw)
		view.Items = append(view.Items, item)
		if turn := presentationInt(presentationFirst(raw["turn_index"], raw["turn_anchor"], raw["source_turn"], raw["to_turn"])); turn > view.CurrentTurn {
			view.CurrentTurn = turn
		}
		itemView := presentationMap(item["view"])
		key := presentationString(itemView["display_turn_key"])
		idx, exists := groupIndex[key]
		if !exists {
			view.Groups = append(view.Groups, presentationTimelineGroup{
				Key: key, TurnText: presentationString(itemView["turn_text"]),
				CreatedText: presentationString(item["created_at"]), Kind: "turn",
				Counts: map[string]int{}, ItemKeys: []string{},
			})
			idx = len(view.Groups) - 1
			groupIndex[key] = idx
		}
		group := &view.Groups[idx]
		rawType := strings.ToLower(presentationString(item["type"]))
		if rawType == "" {
			rawType = presentationString(itemView["normalized_type"])
		}
		group.Counts[rawType]++
		group.ItemKeys = append(group.ItemKeys, presentationString(itemView["key"]))
		group.items = append(group.items, item)
		if group.CreatedText == "" {
			group.CreatedText = presentationString(item["created_at"])
		}
	}
	for i := range view.Groups {
		group := &view.Groups[i]
		if group.Key == "turn:-1" {
			group.Kind = "starter"
		}
		group.ItemCount = len(group.items)
		group.Preview = presentationTimelineGroupPreview(group.items)
	}
	return view
}

// timelineWorldlineTopology reads the worldline graph around the selected
// session, or the current one when nothing is selected.
func (s *Server) timelineWorldlineTopology(ctx context.Context, currentSessionID, selectedSessionID string) worldlineTopologyViewModel {
	currentSessionID = strings.TrimSpace(currentSessionID)
	selectedSessionID = strings.TrimSpace(selectedSessionID)
	anchorSessionID := selectedSessionID
	if anchorSessionID == "" {
		anchorSessionID = currentSessionID
	}
	topologyStore, ok := s.Store.(store.WorldlineTopologySnapshotStore)
	if !ok {
		return emptyWorldlineTopologyViewModel(currentSessionID, selectedSessionID, "topology_snapshot_store_unavailable")
	}
	if anchorSessionID == "" {
		return emptyWorldlineTopologyViewModel(currentSessionID, selectedSessionID, "session_anchor_unavailable")
	}
	snapshot, err := topologyStore.GetWorldlineTopologySnapshot(ctx, anchorSessionID, worldlineTopologyFamilyLimit)
	if err != nil {
		reason := "topology_snapshot_read_unavailable"
		if errors.Is(err, store.ErrNotFound) {
			reason = "current_session_not_routed"
		}
		return emptyWorldlineTopologyViewModel(currentSessionID, selectedSessionID, reason)
	}
	return buildWorldlineTopologyViewModel(snapshot, currentSessionID, selectedSessionID)
}

func presentationTimelineItem(source map[string]any) map[string]any {
	item := presentationCloneMap(source)
	rawType := strings.ToLower(presentationString(item["type"]))
	role := strings.ToLower(presentationString(item["role"]))
	normalized := "user"
	switch rawType {
	case "chat_log":
		if role == "assistant" {
			normalized = "assistant"
		}
	case "kg_triple", "episode":
		normalized = "episode"
	case "direct_evidence", "evidence", "memory":
		normalized = "memory"
	case "pending_artifacts":
		normalized = "pending"
	}
	id := presentationFirst(item["id"], item["source_id"])
	itemKey := presentationFirstString(rawType, "item") + ":" + presentationString(id)
	turn := presentationTurnText(item)
	title := presentationString(item["title"])
	titleCode := ""
	if title == "" {
		switch rawType {
		case "chat_log":
			title = presentationFirstString(role, "chat_log")
		case "memory":
			titleCode = "memoryTitle"
		case "episode":
			titleCode = "episodeTitle"
		case "kg_triple":
			titleCode = "kgTitle"
		case "evidence", "direct_evidence":
			titleCode = "evidenceTitle"
		case "pending_artifacts":
			titleCode = "pendingTitle"
		default:
			titleCode = "itemFallback"
		}
	}
	displayTurnKey := "item:" + itemKey
	if turn != "" {
		displayTurnKey = "turn:" + turn
	}
	editableType := ""
	switch rawType {
	case "memory":
		editableType = "mem"
	case "evidence", "direct_evidence":
		editableType = "de"
	case "kg_triple":
		editableType = "kg"
	}
	item["view"] = map[string]any{
		"key": itemKey, "normalized_type": normalized, "raw_type": rawType,
		"title": title, "title_code": titleCode, "preview": presentationReadableText(presentationFirst(item["preview"], item["summary"], item["content"], item["summary_json"], item["summary_text"], item["evidence_text"])),
		"turn_text": turn, "display_turn_key": displayTurnKey, "source_id": id,
		"editable_type": editableType, "can_edit": editableType != "" && id != nil,
	}
	return item
}

func presentationTimelineGroupPreview(items []map[string]any) string {
	if len(items) == 0 {
		return ""
	}
	priority := func(item map[string]any) int {
		t := strings.ToLower(presentationString(item["type"]))
		r := strings.ToLower(presentationString(item["role"]))
		switch {
		case t == "memory" || t == "evidence" || t == "direct_evidence":
			return 1
		case t == "episode":
			return 2
		case t == "chat_log" && r == "assistant":
			return 3
		default:
			return 4
		}
	}
	ordered := append([]map[string]any(nil), items...)
	sort.SliceStable(ordered, func(i, j int) bool { return priority(ordered[i]) < priority(ordered[j]) })
	for _, item := range ordered {
		view := presentationMap(item["view"])
		text := presentationFirstString(presentationString(view["preview"]), presentationString(view["title"]))
		if text != "" {
			return presentationCompactText(text, 72)
		}
	}
	return ""
}

func presentationReadableText(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		text := strings.TrimSpace(typed)
		if text != "" && (strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[")) {
			var parsed any
			if json.Unmarshal([]byte(text), &parsed) == nil {
				return presentationReadableText(parsed)
			}
		}
		return typed
	case []any:
		parts := []string{}
		for _, item := range typed {
			if text := strings.TrimSpace(presentationReadableText(item)); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		for _, key := range []string{"summary", "text", "content", "memory", "description", "value"} {
			if text := strings.TrimSpace(presentationReadableText(typed[key])); text != "" {
				return text
			}
		}
		encoded, _ := json.MarshalIndent(typed, "", "  ")
		return string(encoded)
	default:
		return fmt.Sprint(value)
	}
}

func presentationCompactText(value string, limit int) string {
	clean := strings.Join(strings.Fields(strings.ReplaceAll(strings.ReplaceAll(value, "**", ""), "#", " ")), " ")
	if len([]rune(clean)) <= limit {
		return clean
	}
	runes := []rune(clean)
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}

func presentationTurnText(item map[string]any) string {
	return presentationString(presentationFirst(item["turn_index"], item["turn_anchor"], item["source_turn"], item["to_turn"]))
}
func presentationMap(value any) map[string]any {
	if out, ok := value.(map[string]any); ok && out != nil {
		return out
	}
	return map[string]any{}
}
func presentationCloneMap(input map[string]any) map[string]any {
	out := make(map[string]any, len(input)+1)
	for key, value := range input {
		out[key] = value
	}
	return out
}
func presentationFirst(values ...any) any {
	for _, value := range values {
		if value != nil && presentationString(value) != "" {
			return value
		}
	}
	return nil
}
func presentationFirstString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
func presentationString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
func presentationFloat(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		n, _ := typed.Float64()
		return n
	case string:
		n, _ := strconv.ParseFloat(typed, 64)
		return n
	}
	return 0
}
func presentationInt(value any) int { return int(presentationFloat(value)) }
