package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestMariaDBReviseHistoricalTurnClearsOnlyThatTurn(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := &mariadbStore{db: db}
	edited := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT source_revision\s+FROM memory_source_revisions\s+WHERE chat_session_id = \? AND turn_index = \? AND lifecycle_state = 'active'`).
		WithArgs("session-1", 10).
		WillReturnRows(sqlmock.NewRows([]string{"source_revision"}))
	// Independent list: every delete is bound to turn 10 alone.
	for _, command := range []struct {
		query string
		args  []driver.Value
	}{
		{`DELETE FROM memories WHERE chat_session_id = ? AND turn_index = ?`, []driver.Value{"session-1", 10}},
		{`DELETE FROM direct_evidence_records WHERE chat_session_id = ? AND source_turn_end = ?`, []driver.Value{"session-1", 10}},
		{`DELETE FROM kg_triples WHERE chat_session_id = ? AND source_turn = ?`, []driver.Value{"session-1", 10}},
		{`DELETE FROM critic_feedback WHERE chat_session_id = ? AND target_type = 'turn' AND target_id = ?`, []driver.Value{"session-1", 10}},
		{`DELETE FROM character_events WHERE chat_session_id = ? AND turn_index = ?`, []driver.Value{"session-1", 10}},
		{`DELETE FROM speaker_attributions WHERE chat_session_id = ? AND source_turn = ?`, []driver.Value{"session-1", 10}},
		{`DELETE FROM entity_identity_artifact_bindings WHERE chat_session_id = ? AND source_turn = ?`, []driver.Value{"session-1", 10}},
		{`DELETE FROM entity_identity_surfaces WHERE chat_session_id = ? AND source_turn = ?`, []driver.Value{"session-1", 10}},
		{`DELETE FROM world_rules WHERE chat_session_id = ? AND source_turn = ? AND pinned = 0 AND user_corrected = 0`, []driver.Value{"session-1", 10}},
		{`DELETE FROM capture_verification_records WHERE chat_session_id = ? AND turn_index = ?`, []driver.Value{"session-1", 10}},
		{`DELETE FROM protagonist_entity_memories WHERE source_chat_session_id = ? AND source_turn_index = ?`, []driver.Value{"session-1", 10}},
		{`DELETE FROM consequence_records WHERE chat_session_id = ? AND source_turn_start = ? AND source_turn_end = ?`, []driver.Value{"session-1", 10, 10}},
		{`DELETE FROM psychology_branches WHERE chat_session_id = ? AND source_turn_start = ? AND source_turn_end = ?`, []driver.Value{"session-1", 10, 10}},
		{`DELETE FROM theme_offscreen_carries WHERE chat_session_id = ? AND source_turn_start = ? AND source_turn_end = ?`, []driver.Value{"session-1", 10, 10}},
	} {
		mock.ExpectExec(regexp.QuoteMeta(command.query)).WithArgs(command.args...).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE chat_logs SET content = ? WHERE chat_session_id = ? AND turn_index = ? AND role = ?`)).
		WithArgs("edited user", "session-1", 10, "user").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE chat_logs SET content = ? WHERE chat_session_id = ? AND turn_index = ? AND role = ?`)).
		WithArgs("edited reply", "session-1", 10, "assistant").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := m.ReviseHistoricalTurn(context.Background(), HistoricalTurnEdit{
		ChatSessionID: "session-1", TurnIndex: 10, UserContent: "edited user", AssistantContent: "edited reply", EditedAt: edited,
	}); err != nil {
		t.Fatalf("ReviseHistoricalTurn: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMariaDBReviseHistoricalTurnRollsBackWithoutTheMessage(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := &mariadbStore{db: db}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT source_revision`).WillReturnRows(sqlmock.NewRows([]string{"source_revision"}))
	for range historicalTurnDeleteCommands("session-1", 4) {
		mock.ExpectExec(`DELETE FROM`).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectExec(`UPDATE chat_logs`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM chat_logs`)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectRollback()
	err = m.ReviseHistoricalTurn(context.Background(), HistoricalTurnEdit{
		ChatSessionID: "session-1", TurnIndex: 4, UserContent: "u", AssistantContent: "a",
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
