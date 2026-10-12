package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// HistoricalTurnEdit carries the new raw text of a turn the user edited in
// RisuAI after later turns were already archived.
type HistoricalTurnEdit struct {
	ChatSessionID    string
	TurnIndex        int
	UserContent      string
	AssistantContent string
	EditedAt         time.Time
}

// HistoricalTurnReviewItem is a record the edited turn shares with other
// turns, so the edit kept it as it was. Reason is "changed_later" when it came
// from the edited turn and a later turn changed it, or "last_changed_here"
// when it came from an earlier turn and the edited turn changed it last.
type HistoricalTurnReviewItem struct {
	Kind      string `json:"kind"`
	ID        int64  `json:"id,omitempty"`
	Label     string `json:"label"`
	TurnIndex int    `json:"turn_index"`
	OtherTurn int    `json:"other_turn,omitempty"`
	Reason    string `json:"reason"`
}

// HistoricalTurnEditStore replaces one past turn's raw text in place and
// clears what was derived from that turn alone, so the turn can be derived
// again. Records other turns built on stay as they are and are returned for
// review.
type HistoricalTurnEditStore interface {
	ReviseHistoricalTurn(ctx context.Context, edit HistoricalTurnEdit) ([]HistoricalTurnReviewItem, error)
}

var _ HistoricalTurnEditStore = (*mariadbStore)(nil)

// historicalTurnDeleteCommands removes rows that belong to turn t only.
// Storylines, pending threads, and entities go only when no other turn touched
// them; status values from turn t are recomputed afterwards. Anything the user
// pinned or corrected is kept.
func historicalTurnDeleteCommands(sid string, t int) []canonicalTailDeleteCommand {
	return []canonicalTailDeleteCommand{
		{`DELETE FROM memories WHERE chat_session_id = ? AND turn_index = ?`, []any{sid, t}},
		{`DELETE FROM direct_evidence_records WHERE chat_session_id = ? AND source_turn_end = ?`, []any{sid, t}},
		{`DELETE FROM kg_triples WHERE chat_session_id = ? AND source_turn = ?`, []any{sid, t}},
		{`DELETE FROM critic_feedback WHERE chat_session_id = ? AND target_type = 'turn' AND target_id = ?`, []any{sid, t}},
		{`DELETE FROM character_events WHERE chat_session_id = ? AND turn_index = ?`, []any{sid, t}},
		{`DELETE FROM speaker_attributions WHERE chat_session_id = ? AND source_turn = ?`, []any{sid, t}},
		{`DELETE FROM entity_identity_artifact_bindings WHERE chat_session_id = ? AND source_turn = ?`, []any{sid, t}},
		{`DELETE FROM entity_identity_surfaces WHERE chat_session_id = ? AND source_turn = ?`, []any{sid, t}},
		{`DELETE FROM world_rules WHERE chat_session_id = ? AND source_turn = ? AND pinned = 0 AND user_corrected = 0`, []any{sid, t}},
		{`DELETE FROM capture_verification_records WHERE chat_session_id = ? AND turn_index = ?`, []any{sid, t}},
		{`DELETE FROM protagonist_entity_memories WHERE source_chat_session_id = ? AND source_turn_index = ?`, []any{sid, t}},
		{`DELETE FROM consequence_records WHERE chat_session_id = ? AND source_turn_start = ? AND source_turn_end = ?`, []any{sid, t, t}},
		{`DELETE FROM psychology_branches WHERE chat_session_id = ? AND source_turn_start = ? AND source_turn_end = ?`, []any{sid, t, t}},
		{`DELETE FROM theme_offscreen_carries WHERE chat_session_id = ? AND source_turn_start = ? AND source_turn_end = ?`, []any{sid, t, t}},
		{`DELETE FROM pending_threads WHERE chat_session_id = ? AND created_turn = ? AND COALESCE(source_turn, created_turn) = ? AND COALESCE(resolved_turn, 0) IN (0, ?) AND pinned = 0 AND user_corrected = 0`, []any{sid, t, t, t}},
		{`DELETE FROM storylines WHERE chat_session_id = ? AND first_turn = ? AND last_turn = ? AND pinned = 0 AND user_corrected = 0`, []any{sid, t, t}},
		{`DELETE FROM entities WHERE chat_session_id = ? AND first_seen_turn = ? AND last_seen_turn = ? AND pinned = 0 AND user_corrected = 0`, []any{sid, t, t}},
		{`DELETE FROM trust_states WHERE chat_session_id = ? AND source_turn = ? AND pinned = 0 AND user_corrected = 0`, []any{sid, t}},
		{`DELETE FROM status_current_values WHERE chat_session_id = ? AND source_turn = ?`, []any{sid, t}},
		{`DELETE FROM status_change_events WHERE chat_session_id = ? AND source_turn = ? AND NULLIF(TRIM(JSON_UNQUOTE(JSON_EXTRACT(evidence_json, '$.source_revision'))), '') IS NULL`, []any{sid, t}},
		{`UPDATE status_effects SET effect_state = 'active', cleared_evidence_json = NULL, cleared_turn = NULL, updated_at = CURRENT_TIMESTAMP(3) WHERE chat_session_id = ? AND cleared_turn = ?`, []any{sid, t}},
		{`DELETE FROM status_effects WHERE chat_session_id = ? AND source_turn = ?`, []any{sid, t}},
	}
}

// historicalTurnReviewQueries find the records turn t shares with other
// turns. Each row is kind, id, label, the other turn, and the reason; pinned
// or corrected records are left out, since the user already owns them. Status
// values count only when no thread or storyline stands for them.
func historicalTurnReviewQueries(sid string, t int) []canonicalTailDeleteCommand {
	return []canonicalTailDeleteCommand{
		{`SELECT 'pending_thread', id, COALESCE(NULLIF(description, ''), thread_key),
			CASE WHEN created_turn = ? THEN GREATEST(COALESCE(source_turn, 0), COALESCE(resolved_turn, 0)) ELSE created_turn END,
			CASE WHEN created_turn = ? THEN 'changed_later' ELSE 'last_changed_here' END
		FROM pending_threads
		WHERE chat_session_id = ? AND pinned = 0 AND user_corrected = 0 AND (
			(created_turn = ? AND (COALESCE(source_turn, 0) > ? OR COALESCE(resolved_turn, 0) > ?))
			OR (created_turn < ? AND (source_turn = ? OR resolved_turn = ?)))
		ORDER BY id`, []any{t, t, sid, t, t, t, t, t, t}},
		{`SELECT 'storyline', id, name,
			CASE WHEN first_turn = ? THEN last_turn ELSE first_turn END,
			CASE WHEN first_turn = ? THEN 'changed_later' ELSE 'last_changed_here' END
		FROM storylines
		WHERE chat_session_id = ? AND pinned = 0 AND user_corrected = 0 AND (
			(first_turn = ? AND last_turn > ?) OR (first_turn > 0 AND first_turn < ? AND last_turn = ?))
		ORDER BY id`, []any{t, t, sid, t, t, t, t}},
		{`SELECT 'status', MIN(event.id), CONCAT(event.owner_id, ' · ', event.status_key), MAX(newer.source_turn), 'changed_later'
		FROM status_change_events event
		JOIN status_change_events newer
		  ON newer.chat_session_id = event.chat_session_id AND newer.status_key = event.status_key
		 AND newer.owner_scope = event.owner_scope AND newer.owner_id = event.owner_id AND newer.source_turn > event.source_turn
		WHERE event.chat_session_id = ? AND event.source_turn = ? AND event.status_key <> 'narrative_state'
		GROUP BY event.owner_scope, event.owner_id, event.status_key
		ORDER BY MIN(event.id)`, []any{sid, t}},
	}
}

func historicalTurnReviewItemsTx(ctx context.Context, tx *sql.Tx, sid string, t int) ([]HistoricalTurnReviewItem, error) {
	items := []HistoricalTurnReviewItem{}
	for _, query := range historicalTurnReviewQueries(sid, t) {
		rows, err := tx.QueryContext(ctx, query.query, query.args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var item HistoricalTurnReviewItem
			var label sql.NullString
			var other sql.NullInt64
			if err := rows.Scan(&item.Kind, &item.ID, &label, &other, &item.Reason); err != nil {
				rows.Close()
				return nil, err
			}
			item.Label, item.OtherTurn, item.TurnIndex = label.String, int(other.Int64), t
			items = append(items, item)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (m *mariadbStore) ReviseHistoricalTurn(ctx context.Context, edit HistoricalTurnEdit) ([]HistoricalTurnReviewItem, error) {
	if err := m.ensureDB(); err != nil {
		return nil, err
	}
	sid := strings.TrimSpace(edit.ChatSessionID)
	t := edit.TurnIndex
	if sid == "" || t <= 0 || strings.TrimSpace(edit.UserContent) == "" || strings.TrimSpace(edit.AssistantContent) == "" {
		return nil, fmt.Errorf("invalid historical turn edit")
	}
	m.memoryDerivationWriteMu.Lock()
	defer m.memoryDerivationWriteMu.Unlock()
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	now := nonZeroTime(edit.EditedAt)
	review, err := historicalTurnReviewItemsTx(ctx, tx, sid, t)
	if err != nil {
		return nil, err
	}
	// The turn's source revisions go first: this also queues deletes for the
	// vectors of its memories, evidence, and world rules.
	if err := invalidateMemorySourcesTx(ctx, tx, sid, t, true, "", LogicalTurnLifecycleInvalidated, "historical_turn_edited", now); err != nil {
		return nil, err
	}
	for _, command := range historicalTurnDeleteCommands(sid, t) {
		if _, err := tx.ExecContext(ctx, command.query, command.args...); err != nil {
			return nil, err
		}
	}
	// Status values, and the pending threads they project, fall back to what
	// the remaining turns say.
	if err := restoreActiveStatusCurrentValuesTx(ctx, tx, sid); err != nil {
		return nil, err
	}
	// Updating in place keeps each row's id and time, so the turn keeps its
	// position in the timeline.
	for _, role := range []struct{ name, content string }{{"user", edit.UserContent}, {"assistant", edit.AssistantContent}} {
		res, err := tx.ExecContext(ctx, `UPDATE chat_logs SET content = ? WHERE chat_session_id = ? AND turn_index = ? AND role = ?`, role.content, sid, t, role.name)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM chat_logs WHERE chat_session_id = ? AND turn_index = ? AND role = ?`, sid, t, role.name).Scan(&count); err != nil {
				return nil, err
			}
			if count == 0 {
				return nil, fmt.Errorf("turn %d has no %s message to edit: %w", t, role.name, ErrNotFound)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	return review, nil
}

// HierarchySummaryRangeDeleter removes the chapter, arc, or saga stored for
// exactly one turn range, once its replacement has been generated.
type HierarchySummaryRangeDeleter interface {
	DeleteHierarchySummaryRange(ctx context.Context, layer, chatSessionID string, fromTurn, toTurn int) (int64, error)
}

var _ HierarchySummaryRangeDeleter = (*mariadbStore)(nil)

func (m *mariadbStore) DeleteHierarchySummaryRange(ctx context.Context, layer, chatSessionID string, fromTurn, toTurn int) (int64, error) {
	if err := m.ensureDB(); err != nil {
		return 0, err
	}
	table := map[string]string{"chapter": "chapter_summaries", "arc": "arc_summaries", "saga": "saga_digests"}[layer]
	if table == "" {
		return 0, fmt.Errorf("unknown hierarchy layer %q", layer)
	}
	res, err := m.db.ExecContext(ctx, `DELETE FROM `+table+` WHERE chat_session_id = ? AND from_turn = ? AND to_turn = ?`, chatSessionID, fromTurn, toTurn)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// DerivedReplayStore clears everything derived from a turn onward while
// keeping the raw text, so those turns can be derived again in order.
type DerivedReplayStore interface {
	ClearDerivedFromTurn(ctx context.Context, chatSessionID string, fromTurn int, now time.Time) error
}

var _ DerivedReplayStore = (*mariadbStore)(nil)

// derivedReplayDeleteCommands is the rollback cleanup without its raw and
// input-log deletes. Records the user pinned or corrected stay.
func derivedReplayDeleteCommands(sid string, t int) []canonicalTailDeleteCommand {
	commands := []canonicalTailDeleteCommand{}
	for _, command := range canonicalTailDeleteCommands(sid, t, false, false) {
		switch {
		case strings.HasPrefix(command.query, "DELETE FROM chat_logs "),
			strings.HasPrefix(command.query, "DELETE FROM effective_input_logs "):
			continue
		case strings.HasPrefix(command.query, "DELETE FROM world_rules "),
			strings.HasPrefix(command.query, "DELETE FROM storylines "),
			strings.HasPrefix(command.query, "DELETE FROM pending_threads "),
			strings.HasPrefix(command.query, "DELETE FROM entities "),
			strings.HasPrefix(command.query, "DELETE FROM trust_states "):
			command.query += " AND pinned = 0 AND user_corrected = 0"
		}
		commands = append(commands, command)
	}
	return commands
}

func (m *mariadbStore) ClearDerivedFromTurn(ctx context.Context, chatSessionID string, fromTurn int, now time.Time) error {
	if err := m.ensureDB(); err != nil {
		return err
	}
	sid := strings.TrimSpace(chatSessionID)
	if sid == "" || fromTurn <= 0 {
		return fmt.Errorf("invalid derived replay")
	}
	m.memoryDerivationWriteMu.Lock()
	defer m.memoryDerivationWriteMu.Unlock()
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := invalidateMemorySourcesTx(ctx, tx, sid, fromTurn, false, "", LogicalTurnLifecycleInvalidated, "derived_replay", nonZeroTime(now)); err != nil {
		return err
	}
	for _, command := range derivedReplayDeleteCommands(sid, fromTurn) {
		if _, err := tx.ExecContext(ctx, command.query, command.args...); err != nil {
			return err
		}
	}
	if err := restoreActiveStatusCurrentValuesTx(ctx, tx, sid); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}
