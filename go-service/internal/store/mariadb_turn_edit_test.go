package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"reflect"
	"regexp"
	"strings"
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
	expectHistoricalTurnReview(mock, "session-1", 10)
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
		{`DELETE FROM pending_threads WHERE chat_session_id = ? AND created_turn = ? AND COALESCE(source_turn, created_turn) = ? AND COALESCE(resolved_turn, 0) IN (0, ?) AND pinned = 0 AND user_corrected = 0`, []driver.Value{"session-1", 10, 10, 10}},
		{`DELETE FROM storylines WHERE chat_session_id = ? AND first_turn = ? AND last_turn = ? AND pinned = 0 AND user_corrected = 0`, []driver.Value{"session-1", 10, 10}},
		{`DELETE FROM entities WHERE chat_session_id = ? AND first_seen_turn = ? AND last_seen_turn = ? AND pinned = 0 AND user_corrected = 0`, []driver.Value{"session-1", 10, 10}},
		{`DELETE FROM trust_states WHERE chat_session_id = ? AND source_turn = ? AND pinned = 0 AND user_corrected = 0`, []driver.Value{"session-1", 10}},
		{`DELETE FROM status_current_values WHERE chat_session_id = ? AND source_turn = ?`, []driver.Value{"session-1", 10}},
		{`DELETE FROM status_change_events WHERE chat_session_id = ? AND source_turn = ? AND NULLIF(TRIM(JSON_UNQUOTE(JSON_EXTRACT(evidence_json, '$.source_revision'))), '') IS NULL`, []driver.Value{"session-1", 10}},
		{`UPDATE status_effects SET effect_state = 'active', cleared_evidence_json = NULL, cleared_turn = NULL, updated_at = CURRENT_TIMESTAMP(3) WHERE chat_session_id = ? AND cleared_turn = ?`, []driver.Value{"session-1", 10}},
		{`DELETE FROM status_effects WHERE chat_session_id = ? AND source_turn = ?`, []driver.Value{"session-1", 10}},
	} {
		mock.ExpectExec(regexp.QuoteMeta(command.query)).WithArgs(command.args...).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec("INSERT INTO status_current_values").WithArgs("session-1").WillReturnResult(sqlmock.NewResult(0, 0))
	expectNarrativePendingSnapshots46(mock, "session-1")
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE chat_logs SET content = ? WHERE chat_session_id = ? AND turn_index = ? AND role = ?`)).
		WithArgs("edited user", "session-1", 10, "user").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE chat_logs SET content = ? WHERE chat_session_id = ? AND turn_index = ? AND role = ?`)).
		WithArgs("edited reply", "session-1", 10, "assistant").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	review, err := m.ReviseHistoricalTurn(context.Background(), HistoricalTurnEdit{
		ChatSessionID: "session-1", TurnIndex: 10, UserContent: "edited user", AssistantContent: "edited reply", EditedAt: edited,
	})
	if err != nil {
		t.Fatalf("ReviseHistoricalTurn: %v", err)
	}
	want := []HistoricalTurnReviewItem{
		{Kind: "pending_thread", ID: 7, Label: "lunch tomorrow", TurnIndex: 10, OtherTurn: 14, Reason: "changed_later"},
		{Kind: "status", ID: 30, Label: "Rina · injury", TurnIndex: 10, OtherTurn: 18, Reason: "changed_later"},
	}
	if !reflect.DeepEqual(review, want) {
		t.Fatalf("review = %#v, want %#v", review, want)
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
	for range historicalTurnReviewQueries("session-1", 4) {
		mock.ExpectQuery(`SELECT '`).WillReturnRows(sqlmock.NewRows([]string{"kind", "id", "label", "other", "reason"}))
	}
	mock.ExpectQuery(`SELECT source_revision`).WillReturnRows(sqlmock.NewRows([]string{"source_revision"}))
	for range historicalTurnDeleteCommands("session-1", 4) {
		mock.ExpectExec(`(DELETE FROM|UPDATE status_effects)`).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectExec("INSERT INTO status_current_values").WillReturnResult(sqlmock.NewResult(0, 0))
	expectNarrativePendingSnapshots46(mock, "session-1")
	mock.ExpectExec(`UPDATE chat_logs`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM chat_logs`)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectRollback()
	_, err = m.ReviseHistoricalTurn(context.Background(), HistoricalTurnEdit{
		ChatSessionID: "session-1", TurnIndex: 4, UserContent: "u", AssistantContent: "a",
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func expectHistoricalTurnReview(mock sqlmock.Sqlmock, sid string, t int) {
	columns := []string{"kind", "id", "label", "other", "reason"}
	// A thread from turn t that turn 14 changed; storylines are all t's own.
	mock.ExpectQuery(`(?s)SELECT 'pending_thread'.*FROM pending_threads.*pinned = 0 AND user_corrected = 0`).
		WithArgs(t, t, sid, t, t, t, t, t, t).
		WillReturnRows(sqlmock.NewRows(columns).AddRow("pending_thread", 7, "lunch tomorrow", 14, "changed_later"))
	mock.ExpectQuery(`(?s)SELECT 'storyline'.*FROM storylines`).
		WithArgs(t, t, sid, t, t, t, t).
		WillReturnRows(sqlmock.NewRows(columns))
	mock.ExpectQuery(`(?s)SELECT 'status'.*FROM status_change_events event.*status_key <> 'narrative_state'`).
		WithArgs(sid, t).
		WillReturnRows(sqlmock.NewRows(columns).AddRow("status", 30, "Rina · injury", 18, "changed_later"))
}

func TestDerivedReplayKeepsRawAndUserOwnedRecords(t *testing.T) {
	commands := derivedReplayDeleteCommands("session-1", 12)
	userOwned := map[string]bool{"world_rules": true, "storylines": true, "pending_threads": true, "entities": true, "trust_states": true}
	seen := map[string]bool{}
	for _, command := range commands {
		if strings.Contains(command.query, "chat_logs") || strings.Contains(command.query, "effective_input_logs") {
			t.Fatalf("replay must keep raw text and input logs: %s", command.query)
		}
		for table := range userOwned {
			if strings.HasPrefix(command.query, "DELETE FROM "+table+" ") {
				seen[table] = true
				if !strings.HasSuffix(command.query, " AND pinned = 0 AND user_corrected = 0") {
					t.Fatalf("replay deletes user-owned %s rows: %s", table, command.query)
				}
			}
		}
	}
	if len(seen) != len(userOwned) {
		t.Fatalf("replay deletes = %v, want all of %v", seen, userOwned)
	}
	// Everything else matches the rollback cleanup, raw and input logs aside.
	if want := len(canonicalTailDeleteCommands("session-1", 12, false, false)) - 2; len(commands) != want {
		t.Fatalf("replay commands = %d, want %d", len(commands), want)
	}
}

func TestMariaDBClearDerivedFromTurnRunsInOneTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := &mariadbStore{db: db}
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT source_revision\s+FROM memory_source_revisions\s+WHERE chat_session_id = \? AND turn_index >= \? AND lifecycle_state = 'active'`).
		WithArgs("session-1", 12).
		WillReturnRows(sqlmock.NewRows([]string{"source_revision"}))
	for _, command := range derivedReplayDeleteCommands("session-1", 12) {
		args := make([]driver.Value, len(command.args))
		for i, arg := range command.args {
			args[i] = arg
		}
		mock.ExpectExec(regexp.QuoteMeta(command.query)).WithArgs(args...).WillReturnResult(sqlmock.NewResult(0, 0))
	}
	mock.ExpectExec("INSERT INTO status_current_values").WithArgs("session-1").WillReturnResult(sqlmock.NewResult(0, 0))
	expectNarrativePendingSnapshots46(mock, "session-1")
	mock.ExpectCommit()
	if err := m.ClearDerivedFromTurn(context.Background(), "session-1", 12, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("ClearDerivedFromTurn: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
