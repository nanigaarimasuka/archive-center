package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// Find and replace over stored memory reads and writes the text columns
// below. The catalog is fixed: table and column names never come from a
// request. Keys that tie rows together (names used as identities, slots,
// lifecycle keys) are not listed, so a replace cannot split what they join.

// MemoryTextColumn is one text column; JSON columns hold their text in
// string values.
type MemoryTextColumn struct {
	Name string
	JSON bool
}

// MemoryTextTable is one table whose text find and replace may change.
type MemoryTextTable struct {
	Kind, Table, SessionColumn, TurnExpr, Where string
	Columns                                     []MemoryTextColumn
	// Extra columns read along, for the caller to recompute derived values.
	Extra []string
}

func memoryTextColumns(names ...string) []MemoryTextColumn {
	out := make([]MemoryTextColumn, 0, len(names))
	for _, name := range names {
		out = append(out, MemoryTextColumn{Name: name, JSON: strings.HasSuffix(name, "_json")})
	}
	return out
}

// MemoryTextSourceTable holds each turn's raw snapshot and stored critic
// result; replaying a turn rebuilds its memory, evidence and precise memory
// from them, so they change together with those rows.
const MemoryTextSourceTable = "memory_source_revisions"

var MemoryTextTables = []MemoryTextTable{
	{Kind: "source", Table: MemoryTextSourceTable, SessionColumn: "chat_session_id", TurnExpr: "turn_index",
		Where:   "lifecycle_state = 'active' AND derived_admission_state = 'committed'",
		Columns: memoryTextColumns("raw_user_content", "raw_assistant_content", "derived_result_json"),
		Extra:   []string{"source_revision", "derived_admission_version", "derived_extractor_version", "derived_index_version"}},
	{Kind: "memories", Table: "memories", SessionColumn: "chat_session_id", TurnExpr: "turn_index", Columns: memoryTextColumns("summary_json")},
	{Kind: "evidence", Table: "direct_evidence_records", SessionColumn: "chat_session_id", TurnExpr: "COALESCE(turn_anchor, source_turn_end, source_turn_start, 0)", Columns: memoryTextColumns("evidence_text")},
	{Kind: "precise", Table: "precise_memory_units", SessionColumn: "chat_session_id", TurnExpr: "COALESCE(source_turn_end, 0)", Columns: memoryTextColumns("evidence_excerpt", "payload_json", "reveal_condition")},
	{Kind: "kg", Table: "kg_triples", SessionColumn: "chat_session_id", TurnExpr: "COALESCE(source_turn, 0)", Columns: memoryTextColumns("subject", "predicate", "object")},
	{Kind: "hierarchy", Table: "episode_summaries", SessionColumn: "chat_session_id", TurnExpr: "COALESCE(to_turn, 0)", Columns: memoryTextColumns("summary_text", "open_loops_json", "relationship_changes_json")},
	{Kind: "hierarchy", Table: "chapter_summaries", SessionColumn: "chat_session_id", TurnExpr: "COALESCE(to_turn, 0)", Columns: memoryTextColumns("chapter_title", "summary_text", "open_loops_json", "relationship_changes_json", "world_changes_json", "callback_candidates_json", "resume_text")},
	{Kind: "hierarchy", Table: "arc_summaries", SessionColumn: "chat_session_id", TurnExpr: "COALESCE(to_turn, 0)", Columns: memoryTextColumns("arc_name", "core_conflict", "active_promises_json", "unresolved_debts_json", "resolved_payoffs_json", "callback_candidates_json", "future_payoff_candidates_json", "irreversible_turns_json", "callback_debts_json", "relationship_pivots_json", "arc_resume_text")},
	{Kind: "hierarchy", Table: "saga_digests", SessionColumn: "chat_session_id", TurnExpr: "COALESCE(to_turn, 0)", Columns: memoryTextColumns("era_label", "saga_summary", "persistent_facts_json", "never_drop_candidates_json", "resume_pack_text")},
	{Kind: "trust", Table: "storylines", SessionColumn: "chat_session_id", TurnExpr: "COALESCE(last_turn, 0)", Columns: memoryTextColumns("name", "current_context", "entities_json", "ongoing_tensions_json")},
	{Kind: "trust", Table: "pending_threads", SessionColumn: "chat_session_id", TurnExpr: "COALESCE(source_turn, created_turn, 0)", Columns: memoryTextColumns("description", "hook_metadata_json")},
	{Kind: "trust", Table: "trust_states", SessionColumn: "chat_session_id", TurnExpr: "COALESCE(source_turn, 0)", Columns: memoryTextColumns("reason_json")},
	{Kind: "world", Table: "world_rules", SessionColumn: "chat_session_id", TurnExpr: "COALESCE(source_turn, 0)", Columns: memoryTextColumns("value_json")},
	{Kind: "entities", Table: "character_states", SessionColumn: "chat_session_id", TurnExpr: "COALESCE(turn_index, 0)", Columns: memoryTextColumns("appearance_json", "personality_json", "status_json", "relationships_json", "speech_style_json")},
	{Kind: "entities", Table: "character_events", SessionColumn: "chat_session_id", TurnExpr: "COALESCE(turn_index, 0)", Columns: memoryTextColumns("details_json")},
	{Kind: "entities", Table: "protagonist_entity_memories", SessionColumn: "source_chat_session_id", TurnExpr: "COALESCE(source_turn_index, 0)", Columns: memoryTextColumns("memory_text", "evidence_excerpt")},
	{Kind: "entities", Table: "status_current_values", SessionColumn: "chat_session_id", TurnExpr: "COALESCE(source_turn, 0)", Columns: memoryTextColumns("value_json")},
	{Kind: "entities", Table: "entities", SessionColumn: "chat_session_id", TurnExpr: "COALESCE(last_seen_turn, 0)", Columns: memoryTextColumns("description")},
}

// MemoryTextTableByName finds a catalog table.
func MemoryTextTableByName(name string) (MemoryTextTable, bool) {
	for _, table := range MemoryTextTables {
		if table.Table == name {
			return table, true
		}
	}
	return MemoryTextTable{}, false
}

// MemoryTextRow is one row's text columns (and extra columns) as stored.
type MemoryTextRow struct {
	Table  string
	ID     int64
	Turn   int
	Values map[string]string
	Extra  map[string]string
}

// MemoryTextStore reads and writes the catalog's text.
type MemoryTextStore interface {
	ListMemoryTextRows(ctx context.Context, chatSessionID string) ([]MemoryTextRow, error)
	// UpdateMemoryTextRow writes after over the row's columns only when each
	// column in before still holds that value; it reports false otherwise.
	UpdateMemoryTextRow(ctx context.Context, chatSessionID, table string, id int64, before, after map[string]string) (bool, error)
}

var _ MemoryTextStore = (*mariadbStore)(nil)

func (m *mariadbStore) ListMemoryTextRows(ctx context.Context, chatSessionID string) ([]MemoryTextRow, error) {
	if err := m.ensureDB(); err != nil {
		return nil, err
	}
	sid := strings.TrimSpace(chatSessionID)
	out := []MemoryTextRow{}
	for _, table := range MemoryTextTables {
		names := []string{}
		for _, column := range table.Columns {
			names = append(names, "COALESCE(CAST("+column.Name+" AS CHAR), '')")
		}
		for _, column := range table.Extra {
			names = append(names, "COALESCE(CAST("+column+" AS CHAR), '')")
		}
		query := "SELECT id, " + table.TurnExpr + ", " + strings.Join(names, ", ") + " FROM " + table.Table + " WHERE " + table.SessionColumn + " = ?"
		if table.Where != "" {
			query += " AND " + table.Where
		}
		rows, err := m.db.QueryContext(ctx, query+" ORDER BY id", sid)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", table.Table, err)
		}
		for rows.Next() {
			var id int64
			var turn sql.NullInt64
			values := make([]string, len(table.Columns)+len(table.Extra))
			dest := []any{&id, &turn}
			for i := range values {
				dest = append(dest, &values[i])
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				return nil, fmt.Errorf("%s: %w", table.Table, err)
			}
			row := MemoryTextRow{Table: table.Table, ID: id, Turn: int(turn.Int64), Values: map[string]string{}, Extra: map[string]string{}}
			for i, column := range table.Columns {
				row.Values[column.Name] = values[i]
			}
			for i, column := range table.Extra {
				row.Extra[column] = values[len(table.Columns)+i]
			}
			out = append(out, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", table.Table, err)
		}
	}
	return out, nil
}

// memoryTextWritable lists the columns a write may set: the text columns and,
// for the source table, the hash of its stored result.
func memoryTextWritable(table MemoryTextTable) map[string]bool {
	out := map[string]bool{}
	for _, column := range table.Columns {
		out[column.Name] = true
	}
	if table.Table == MemoryTextSourceTable {
		out["derived_result_hash"] = true
	}
	return out
}

func (m *mariadbStore) UpdateMemoryTextRow(ctx context.Context, chatSessionID, tableName string, id int64, before, after map[string]string) (bool, error) {
	if err := m.ensureDB(); err != nil {
		return false, err
	}
	table, ok := MemoryTextTableByName(tableName)
	if !ok || id <= 0 || len(after) == 0 {
		return false, fmt.Errorf("invalid memory text update")
	}
	writable := memoryTextWritable(table)
	sets, args := []string{}, []any{}
	for _, name := range sortedMemoryTextKeys(after) {
		if !writable[name] {
			return false, fmt.Errorf("column %s of %s is not writable", name, table.Table)
		}
		sets = append(sets, name+" = ?")
		args = append(args, after[name])
	}
	conds := []string{"id = ?", table.SessionColumn + " = ?"}
	args = append(args, id, strings.TrimSpace(chatSessionID))
	for _, name := range sortedMemoryTextKeys(before) {
		if !writable[name] {
			return false, fmt.Errorf("column %s of %s is not writable", name, table.Table)
		}
		conds = append(conds, "COALESCE(CAST("+name+" AS CHAR), '') = ?")
		args = append(args, before[name])
	}
	m.memoryDerivationWriteMu.Lock()
	defer m.memoryDerivationWriteMu.Unlock()
	res, err := m.db.ExecContext(ctx, "UPDATE "+table.Table+" SET "+strings.Join(sets, ", ")+" WHERE "+strings.Join(conds, " AND "), args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func sortedMemoryTextKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// EntityIdentityLabelWriter renames an identity: the label it is shown by.
type EntityIdentityLabelWriter interface {
	SetEntityIdentityLabel(ctx context.Context, chatSessionID, stableEntityID, label string) error
}

var _ EntityIdentityLabelWriter = (*mariadbStore)(nil)

func (m *mariadbStore) SetEntityIdentityLabel(ctx context.Context, chatSessionID, stableEntityID, label string) error {
	if err := m.ensureDB(); err != nil {
		return err
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return fmt.Errorf("empty identity label")
	}
	res, err := m.db.ExecContext(ctx, `UPDATE entity_identities SET canonical_label = ?, updated_at = CURRENT_TIMESTAMP(3) WHERE chat_session_id = ? AND stable_entity_id = ?`,
		label, strings.TrimSpace(chatSessionID), strings.TrimSpace(stableEntityID))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
