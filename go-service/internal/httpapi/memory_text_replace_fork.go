package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/risulongmemory/archive-center-go/internal/store"
	"github.com/risulongmemory/archive-center-go/internal/vector"
)

// Find and replace over stored memory. The plugin applies the same pattern it
// used on the RisuAI chat to the text listed here, so a renamed name or a
// fixed spelling reads the same in the chat and in every memory. Nothing is
// derived again by a model: the changed rows are written as they are, each
// turn whose stored critic result changed is replayed from it, and the
// changed vectors are embedded again.

const memoryTextReplaceContract = "memory_text_replace.v1"

// memoryTextSkippedKeys are JSON keys whose values identify rather than
// describe: changing one would split records it joins.
var memoryTextSkippedKeys = map[string]bool{
	"id": true, "status": true, "transition": true, "state_slot": true, "claim_scope": true,
	"perspective_owner": true, "owner": true, "owner_entity_name": true, "name": true, "aliases": true,
	"character_name": true, "subject": true, "kind": true, "type": true, "role": true, "scope": true,
	"category": true, "visibility": true, "language": true, "mode": true, "truth_scope": true,
	"epistemic_mode": true, "memory_kind": true, "memory_subtype": true, "source": true,
	"source_fields": true, "hook_type": true, "gender": true, "species": true, "unit": true,
	"target_kind": true, "anchor": true, "calendar": true,
}

func memoryTextKeySkipped(key string) bool {
	if memoryTextSkippedKeys[key] {
		return true
	}
	for _, suffix := range []string{"_id", "_ids", "_key", "_keys", "_hash", "_ref", "_refs", "_version"} {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return false
}

type memoryTextField struct {
	Ref    string `json:"ref"`
	Kind   string `json:"kind"`
	Table  string `json:"table"`
	ID     int64  `json:"id"`
	Turn   int    `json:"turn"`
	Column string `json:"column"`
	Path   string `json:"path,omitempty"`
	Text   string `json:"text"`
}

func memoryTextRef(table string, id int64, column, path string) string {
	return table + "|" + strconv.FormatInt(id, 10) + "|" + column + "|" + path
}

func parseMemoryTextRef(ref string) (table string, id int64, column, path string, ok bool) {
	parts := strings.SplitN(ref, "|", 4)
	if len(parts) != 4 {
		return "", 0, "", "", false
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		return "", 0, "", "", false
	}
	return parts[0], id, parts[2], parts[3], true
}

func memoryTextPointerToken(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

// walkMemoryText calls emit for every string value under value that a
// replace may change, with its JSON pointer.
func walkMemoryText(value any, path string, emit func(path, text string)) {
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if memoryTextKeySkipped(key) {
				continue
			}
			walkMemoryText(v[key], path+"/"+memoryTextPointerToken(key), emit)
		}
	case []any:
		for i, item := range v {
			walkMemoryText(item, path+"/"+strconv.Itoa(i), emit)
		}
	case string:
		if strings.TrimSpace(v) != "" {
			emit(path, v)
		}
	}
}

// setMemoryText replaces the string at pointer when it still reads before.
func setMemoryText(root any, pointer, before, after string) bool {
	tokens := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	current := root
	for i, raw := range tokens {
		token := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		last := i == len(tokens)-1
		switch node := current.(type) {
		case map[string]any:
			if memoryTextKeySkipped(token) {
				return false
			}
			if last {
				if text, ok := node[token].(string); ok && text == before {
					node[token] = after
					return true
				}
				return false
			}
			current = node[token]
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(node) {
				return false
			}
			if last {
				if text, ok := node[index].(string); ok && text == before {
					node[index] = after
					return true
				}
				return false
			}
			current = node[index]
		default:
			return false
		}
	}
	return false
}

func memoryTextFields(rows []store.MemoryTextRow) []memoryTextField {
	out := []memoryTextField{}
	for _, row := range rows {
		table, ok := store.MemoryTextTableByName(row.Table)
		if !ok {
			continue
		}
		for _, column := range table.Columns {
			raw := row.Values[column.Name]
			add := func(path, text string) {
				out = append(out, memoryTextField{
					Ref: memoryTextRef(row.Table, row.ID, column.Name, path), Kind: table.Kind, Table: row.Table,
					ID: row.ID, Turn: row.Turn, Column: column.Name, Path: path, Text: text,
				})
			}
			if !column.JSON {
				if strings.TrimSpace(raw) != "" {
					add("", raw)
				}
				continue
			}
			var decoded any
			if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
				walkMemoryText(decoded, "", add)
			}
		}
	}
	return out
}

type memoryTextFieldsRequest struct {
	ChatSessionID string `json:"chat_session_id"`
	// Contains keeps only text with this literal (a plain search); empty
	// keeps every field (a regular expression is matched by the plugin).
	Contains      string `json:"contains,omitempty"`
	CaseSensitive bool   `json:"case_sensitive,omitempty"`
}

func (s *Server) handleMemoryTextFields(w http.ResponseWriter, r *http.Request) {
	var req memoryTextFieldsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	sid := strings.TrimSpace(req.ChatSessionID)
	if sid == "" {
		writeBadRequest(w, "chat_session_id is required")
		return
	}
	reader, ok := s.Store.(store.MemoryTextStore)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "error", "detail": "memory_text_store_unavailable"})
		return
	}
	rows, err := reader.ListMemoryTextRows(r.Context(), sid)
	if err != nil {
		writeInternalError(w, err.Error())
		return
	}
	fields := memoryTextFields(rows)
	if needle := req.Contains; needle != "" {
		if !req.CaseSensitive {
			needle = strings.ToLower(needle)
		}
		kept := fields[:0]
		for _, field := range fields {
			text := field.Text
			if !req.CaseSensitive {
				text = strings.ToLower(text)
			}
			if strings.Contains(text, needle) {
				kept = append(kept, field)
			}
		}
		fields = kept
	}
	characters := []map[string]any{}
	if catalog, err := s.characterIdentityCatalogForSession(r.Context(), sid); err == nil {
		for id, identity := range catalog.Identities {
			characters = append(characters, map[string]any{"stable_entity_id": id, "label": identity.CanonicalLabel})
		}
		sort.Slice(characters, func(i, j int) bool {
			return fmt.Sprint(characters[i]["label"]) < fmt.Sprint(characters[j]["label"])
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "chat_session_id": sid, "fields": fields, "characters": characters})
}

type memoryTextChange struct {
	Ref    string `json:"ref"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type memoryTextRename struct {
	StableEntityID string `json:"stable_entity_id"`
	To             string `json:"to"`
}

type memoryTextApplyRequest struct {
	ChatSessionID string             `json:"chat_session_id"`
	Changes       []memoryTextChange `json:"changes"`
	Renames       []memoryTextRename `json:"renames"`
}

type memoryTextRowKey struct {
	table string
	id    int64
}

func (s *Server) handleMemoryTextApply(w http.ResponseWriter, r *http.Request) {
	if !s.usesShadowWriteStore() {
		writeShadowGuard(w, "POST /admin/memory-text/apply")
		return
	}
	var req memoryTextApplyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20)).Decode(&req); err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	sid := strings.TrimSpace(req.ChatSessionID)
	if sid == "" {
		writeBadRequest(w, "chat_session_id is required")
		return
	}
	textStore, ok := s.Store.(store.MemoryTextStore)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "error", "detail": "memory_text_store_unavailable"})
		return
	}
	result, err := s.applyMemoryTextChanges(r.Context(), sid, textStore, req.Changes)
	if err != nil {
		writeInternalError(w, err.Error())
		return
	}
	result["renames"] = s.applyMemoryTextRenames(r.Context(), sid, req.Renames)
	result["vector_refresh"] = s.refreshMemoryTextVectors(r.Context(), sid, result)
	s.saveAuditLogBestEffort(r.Context(), &store.AuditLog{
		ChatSessionID: sid, EventType: "memory_text_replace", TargetType: "memory_text",
		Summary:     fmt.Sprintf("replaced text in %d fields", intFromAny(result["applied"], 0)),
		DetailsJSON: mustCompactJSON(map[string]any{"applied": result["applied"], "stale": result["stale"], "renames": result["renames"]}),
		Source:      s.storeWriteSource(),
	})
	result["status"] = "ok"
	if len(result["stale"].([]string)) > 0 || len(result["failed"].([]map[string]any)) > 0 {
		result["status"] = "partial_error"
	}
	result["chat_session_id"] = sid
	writeJSON(w, http.StatusOK, result)
}

// applyMemoryTextChanges writes the changes row by row. A row is written only
// while every changed column still holds what the plugin read; otherwise its
// changes are reported stale. The changed turns and world rules come back for
// the vector refresh.
func (s *Server) applyMemoryTextChanges(ctx context.Context, sid string, textStore store.MemoryTextStore, changes []memoryTextChange) (map[string]any, error) {
	byRow := map[memoryTextRowKey][]memoryTextChange{}
	order := []memoryTextRowKey{}
	stale := []string{}
	for _, change := range changes {
		table, id, _, _, ok := parseMemoryTextRef(change.Ref)
		if _, known := store.MemoryTextTableByName(table); !ok || !known || change.Before == change.After {
			stale = append(stale, change.Ref)
			continue
		}
		key := memoryTextRowKey{table, id}
		if _, seen := byRow[key]; !seen {
			order = append(order, key)
		}
		byRow[key] = append(byRow[key], change)
	}
	rows, err := textStore.ListMemoryTextRows(ctx, sid)
	if err != nil {
		return nil, err
	}
	current := map[memoryTextRowKey]store.MemoryTextRow{}
	for _, row := range rows {
		current[memoryTextRowKey{row.Table, row.ID}] = row
	}
	applied, failed := 0, []map[string]any{}
	turns, worldRules := map[int]bool{}, map[int64]bool{}
	for _, key := range order {
		row, exists := current[key]
		rowChanges := byRow[key]
		if !exists {
			for _, change := range rowChanges {
				stale = append(stale, change.Ref)
			}
			continue
		}
		table, _ := store.MemoryTextTableByName(key.table)
		before, after, done := map[string]string{}, map[string]string{}, []string{}
		decoded := map[string]any{}
		for _, change := range rowChanges {
			_, _, column, path, _ := parseMemoryTextRef(change.Ref)
			isJSON := false
			for _, c := range table.Columns {
				if c.Name == column {
					isJSON = c.JSON
				}
			}
			if !isJSON {
				text, written := after[column]
				if !written {
					text = row.Values[column]
				}
				if path != "" || text != change.Before {
					stale = append(stale, change.Ref)
					continue
				}
				before[column], after[column] = row.Values[column], change.After
				done = append(done, change.Ref)
				continue
			}
			if decoded[column] == nil {
				var value any
				if err := json.Unmarshal([]byte(row.Values[column]), &value); err != nil {
					stale = append(stale, change.Ref)
					continue
				}
				decoded[column] = value
			}
			if path == "" || !setMemoryText(decoded[column], path, change.Before, change.After) {
				stale = append(stale, change.Ref)
				continue
			}
			before[column] = row.Values[column]
			done = append(done, change.Ref)
		}
		for column, value := range decoded {
			if _, changed := before[column]; changed {
				after[column] = mustCompactJSON(value)
			}
		}
		if len(after) == 0 {
			continue
		}
		if key.table == store.MemoryTextSourceTable {
			if resultJSON, changed := after["derived_result_json"]; changed {
				// Replay re-encodes the decoded result the same way, so this is
				// the text its hash is checked against.
				after["derived_result_hash"] = memoryAdmissionResultHashFromCanonicalJSON(
					row.Extra["source_revision"], resultJSON, row.Extra["derived_admission_version"],
					row.Extra["derived_extractor_version"], row.Extra["derived_index_version"])
			}
		}
		written, err := textStore.UpdateMemoryTextRow(ctx, sid, key.table, key.id, before, after)
		if err != nil || !written {
			for _, ref := range done {
				if err != nil {
					failed = append(failed, map[string]any{"ref": ref, "detail": err.Error()})
				} else {
					stale = append(stale, ref)
				}
			}
			continue
		}
		applied += len(done)
		switch table.Kind {
		case "source", "memories", "evidence", "precise":
			turns[row.Turn] = true
		case "world":
			worldRules[key.id] = true
		}
	}
	return map[string]any{
		"applied": applied, "stale": stale, "failed": failed,
		"changed_turns": sortedIntKeys(turns), "changed_world_rules": sortedInt64Keys(worldRules),
	}, nil
}

func sortedIntKeys(values map[int]bool) []int {
	out := []int{}
	for value := range values {
		if value > 0 {
			out = append(out, value)
		}
	}
	sort.Ints(out)
	return out
}

func sortedInt64Keys(values map[int64]bool) []int64 {
	out := []int64{}
	for value := range values {
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// applyMemoryTextRenames renames characters whose shown name the replace
// changed. A name another character already has merges into it (the link
// "이름 합치기" makes); a new name becomes the character's label, and is
// added as a name it is found by. Stored rows keep the names they were
// written with: reads show them under the label.
func (s *Server) applyMemoryTextRenames(ctx context.Context, sid string, renames []memoryTextRename) []map[string]any {
	results := []map[string]any{}
	if len(renames) == 0 {
		return results
	}
	catalog, err := s.characterIdentityCatalogForSession(ctx, sid)
	if err != nil {
		for _, rename := range renames {
			results = append(results, map[string]any{"stable_entity_id": rename.StableEntityID, "status": "failed", "detail": err.Error()})
		}
		return results
	}
	linker, canLink := s.Store.(store.EntityIdentityLinkWriter)
	labeler, canLabel := s.Store.(store.EntityIdentityLabelWriter)
	surfacer, canSurface := s.Store.(interface {
		SaveEntityIdentitySurface(context.Context, *store.EntityIdentitySurface) error
	})
	for _, rename := range renames {
		id, to := strings.TrimSpace(rename.StableEntityID), strings.TrimSpace(rename.To)
		identity, exists := catalog.Identities[id]
		result := map[string]any{"stable_entity_id": id, "from": identity.CanonicalLabel, "to": to}
		if !exists || to == "" {
			result["status"], result["detail"] = "failed", "unknown character or empty name"
			results = append(results, result)
			continue
		}
		targetID := ""
		for otherID, other := range catalog.Identities {
			if otherID != id && comparableEntityKey(other.CanonicalLabel) == comparableEntityKey(to) {
				targetID = otherID
				break
			}
		}
		if targetID != "" && s.characterIdentityRoot(ctx, sid, targetID) == s.characterIdentityRoot(ctx, sid, id) {
			result["status"], result["target_entity_id"] = "already_merged", targetID
			results = append(results, result)
			continue
		}
		if targetID != "" {
			targetID = s.characterIdentityRoot(ctx, sid, targetID)
			if !canLink {
				result["status"], result["detail"] = "failed", "entity identity links are not writable"
			} else {
				link := characterIdentityManualLink(sid, id, targetID, store.EntityIdentityLinkStateReviewed)
				if err := linker.SaveEntityIdentityLink(ctx, &link); err != nil {
					result["status"], result["detail"] = "failed", err.Error()
				} else {
					result["status"], result["target_entity_id"] = "merged", targetID
				}
			}
			results = append(results, result)
			continue
		}
		if !canLabel || !canSurface {
			result["status"], result["detail"] = "failed", "entity identities are not writable"
			results = append(results, result)
			continue
		}
		now := time.Now().UTC()
		key := comparableEntityKey(to)
		surfaceKey := entityIdentityIdempotencyKey(memoryTextReplaceContract, id, key)
		surface := store.EntityIdentitySurface{
			SurfaceID: entityIdentityStableID("surface", sid, surfaceKey), StableEntityID: id,
			ChatSessionID: sid, IdentityNamespace: identity.IdentityNamespace, SurfaceKind: "display_name",
			SurfaceText: to, NormalizedSurface: key, Scope: store.EntityIdentitySurfaceScopeCurrent,
			SourceContract: memoryTextReplaceContract, SourceRevision: memoryTextReplaceContract + ":" + id + ":" + key,
			SourceSpanStart: -1, SourceSpanEnd: -1, ReviewState: store.EntityIdentityReviewStateReviewed,
			IdempotencyKey: surfaceKey, CreatedAt: now, UpdatedAt: now,
		}
		if err := surfacer.SaveEntityIdentitySurface(ctx, &surface); err != nil {
			result["status"], result["detail"] = "failed", err.Error()
		} else if err := labeler.SetEntityIdentityLabel(ctx, sid, id, to); err != nil {
			result["status"], result["detail"] = "failed", err.Error()
		} else {
			result["status"] = "renamed"
		}
		results = append(results, result)
	}
	return results
}

// refreshMemoryTextVectors removes the vectors of what changed and starts a
// reindex, which embeds exactly the rows left without one: each changed turn
// is replayed from its stored result, each changed world rule embedded.
func (s *Server) refreshMemoryTextVectors(ctx context.Context, sid string, result map[string]any) map[string]any {
	turns, _ := result["changed_turns"].([]int)
	rules, _ := result["changed_world_rules"].([]int64)
	out := map[string]any{"status": "skipped", "turns": len(turns), "world_rules": len(rules)}
	if len(turns) == 0 && len(rules) == 0 {
		out["reason"] = "nothing_embedded_changed"
		return out
	}
	deleter, ok := s.Vector.(vector.DocumentDeleter)
	if !ok || s.Store == nil {
		out["reason"] = "vector_store_unavailable"
		return out
	}
	// Without an embedder the reindex could not fill what is removed; the old
	// vectors stay until one is configured.
	request := map[string]any{"chat_session_id": sid, "background": true, "client_meta": map[string]any{"source": memoryTextReplaceContract}}
	if !s.completeTurnExtractionConfig(mapFromAny(request["client_meta"])).Embedder.hasConfig() {
		out["reason"] = "embedder_not_configured"
		return out
	}
	turnSet := map[int]bool{}
	for _, turn := range turns {
		turnSet[turn] = true
	}
	ids := []string{}
	if memories, err := s.Store.ListMemories(ctx, sid, 0, 0); err == nil {
		for _, mem := range memories {
			if turnSet[mem.TurnIndex] {
				ids = append(ids, memoryVectorDocumentID(sid, mem), rollbackVectorDocumentAlias("memory", sid, mem.ID), rollbackVectorDocumentLegacyAlias("memory", mem.ID))
			}
		}
	}
	if evidence, err := s.Store.ListEvidence(ctx, sid); err == nil {
		for _, item := range evidence {
			turn := item.TurnAnchor
			if turn == 0 {
				turn = item.SourceTurnEnd
			}
			if turn == 0 {
				turn = item.SourceTurnStart
			}
			if turnSet[turn] {
				ids = append(ids, derivedArtifactVectorDocumentID("evidence", sid, item.ID), rollbackVectorDocumentAlias("evidence", sid, item.ID), rollbackVectorDocumentLegacyAlias("evidence", item.ID))
			}
		}
	}
	if reader, ok := s.Store.(store.GeneralVectorPreciseMemoryReader); ok {
		if units, err := reader.ListGeneralVectorPreciseMemoryUnits(ctx, sid); err == nil {
			for _, unit := range units {
				if turnSet[unit.SourceTurnEnd] && strings.TrimSpace(unit.UnitID) != "" {
					ids = append(ids, "precise_memory:"+sid+":"+strings.TrimSpace(unit.UnitID))
				}
			}
		}
	}
	for _, id := range rules {
		ids = append(ids, derivedArtifactVectorDocumentID("world_rule", sid, id), rollbackVectorDocumentAlias("world_rule", sid, id), rollbackVectorDocumentLegacyAlias("world_rule", id))
	}
	kept := []string{}
	for _, id := range uniqueNonEmptyStrings(ids) {
		kept = append(kept, id)
	}
	if err := deleter.DeleteDocuments(ctx, kept); err != nil {
		out["status"], out["reason"], out["detail"] = "failed", "vector_delete_failed", err.Error()
		return out
	}
	if s.AdminJobs == nil {
		s.AdminJobs = newAdminJobManager()
	}
	job := s.AdminJobs.start("reindex", sid, request, s.withdrawVectorWorkOnCancel(func(jobCtx context.Context, progress adminJobProgressFunc) (map[string]any, error) {
		return s.runAdminReindexJob(jobCtx, sid, request, progress)
	}))
	out["status"], out["vectors_removed"], out["job_id"] = "queued", len(kept), job["job_id"]
	return out
}
