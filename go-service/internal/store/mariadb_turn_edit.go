package store

import (
	"context"
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

// HistoricalTurnEditStore replaces one past turn's raw text in place and
// clears what was derived from that turn alone, so the turn can be derived
// again. Later turns and the states they built on stay as they are.
type HistoricalTurnEditStore interface {
	ReviseHistoricalTurn(ctx context.Context, edit HistoricalTurnEdit) error
}

var _ HistoricalTurnEditStore = (*mariadbStore)(nil)

// historicalTurnDeleteCommands removes rows that belong to turn t only.
// Rows that later turns build on (entities and identities, trust, storylines,
// pending threads, status values) are kept; user-pinned or corrected world
// rules are kept too.
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
	}
}

func (m *mariadbStore) ReviseHistoricalTurn(ctx context.Context, edit HistoricalTurnEdit) error {
	if err := m.ensureDB(); err != nil {
		return err
	}
	sid := strings.TrimSpace(edit.ChatSessionID)
	t := edit.TurnIndex
	if sid == "" || t <= 0 || strings.TrimSpace(edit.UserContent) == "" || strings.TrimSpace(edit.AssistantContent) == "" {
		return fmt.Errorf("invalid historical turn edit")
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
	now := nonZeroTime(edit.EditedAt)
	// The turn's source revisions go first: this also queues deletes for the
	// vectors of its memories, evidence, and world rules.
	if err := invalidateMemorySourcesTx(ctx, tx, sid, t, true, "", LogicalTurnLifecycleInvalidated, "historical_turn_edited", now); err != nil {
		return err
	}
	for _, command := range historicalTurnDeleteCommands(sid, t) {
		if _, err := tx.ExecContext(ctx, command.query, command.args...); err != nil {
			return err
		}
	}
	// Updating in place keeps each row's id and time, so the turn keeps its
	// position in the timeline.
	for _, role := range []struct{ name, content string }{{"user", edit.UserContent}, {"assistant", edit.AssistantContent}} {
		res, err := tx.ExecContext(ctx, `UPDATE chat_logs SET content = ? WHERE chat_session_id = ? AND turn_index = ? AND role = ?`, role.content, sid, t, role.name)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM chat_logs WHERE chat_session_id = ? AND turn_index = ? AND role = ?`, sid, t, role.name).Scan(&count); err != nil {
				return err
			}
			if count == 0 {
				return fmt.Errorf("turn %d has no %s message to edit: %w", t, role.name, ErrNotFound)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
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
