package store

import (
	"context"
	"github.com/DATA-DOG/go-sqlmock"
	"strings"
	"testing"
	"time"
)

func TestAdmissionVectorReplayRecordsOnlyOperationsItQueued(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 10, 15, 1, 0, 0, 0, time.UTC)
	recorder := &MemoryVectorOperationRecorder{}
	ctx := WithMemoryVectorOperationRecorder(WithMemoryAdmissionVectorReplay(context.Background(), true, false), recorder)
	for i, previous := range []string{"completed", "needs_embedding"} {
		item := &MemoryVectorOutboxItem{
			OperationKey: strings.Repeat(string(rune('a'+i)), 64), Operation: "upsert",
			ChatSessionID: "session", SourceRevision: "revision",
			DocumentID: "memory:session:17", DocumentJSON: `{"ID":"memory:session:17"}`,
			EmbeddingReady: false, RequiredSourceState: "active", Status: "needs_embedding",
			CreatedAt: now, UpdatedAt: now,
		}
		mock.ExpectQuery("SELECT o.id, o.operation").
			WithArgs(item.OperationKey).
			WillReturnRows(sqlmock.NewRows([]string{
				"id", "operation", "chat_session_id", "source_revision", "document_id",
				"required_source_state", "status", "lease_until", "lifecycle_state",
			}).AddRow(44, item.Operation, item.ChatSessionID, item.SourceRevision,
				item.DocumentID, item.RequiredSourceState, previous, nil, "active"))
		mock.ExpectQuery(`SELECT COUNT\(\*\)`).
			WithArgs(item.ChatSessionID, item.DocumentID, int64(44)).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectExec("UPDATE memory_vector_outbox").
			WithArgs(item.DocumentJSON, false, "needs_embedding", now, item.OperationKey, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
		if _, err := enqueueAdmissionVectorOperation(ctx, db, item); err != nil {
			t.Fatal(err)
		}
	}
	if got := recorder.OperationKeys(); len(got) != 1 || got[0] != strings.Repeat("a", 64) {
		t.Fatalf("recorded %v, want only the operation brought back from completed", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWithdrawMemoryVectorOperationsRejectsOnlyWaitingOnes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectExec(`UPDATE memory_vector_outbox\s+SET status = 'stale_rejected'.*operation_key IN \(\?,\?\)\s+AND status IN \('pending', 'retryable', 'needs_embedding'\)`).
		WithArgs("admin_job_cancelled", sqlmock.AnyArg(), "k1", "k2").
		WillReturnResult(sqlmock.NewResult(0, 1))
	withdrawn, err := (&mariadbStore{db: db}).WithdrawMemoryVectorOperations(context.Background(), []string{"k1", "k2"}, "admin_job_cancelled")
	if err != nil || withdrawn != 1 {
		t.Fatalf("withdrawn=%d err=%v", withdrawn, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCountMemoryVectorOperationStatesGroupsByStatus(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(`SELECT status, COUNT\(\*\)\s+FROM memory_vector_outbox\s+WHERE operation_key IN \(\?,\?\)`).
		WithArgs("k1", "k2").
		WillReturnRows(sqlmock.NewRows([]string{"status", "count"}).AddRow("completed", 1).AddRow("needs_embedding", 1))
	counts, err := (&mariadbStore{db: db}).CountMemoryVectorOperationStates(context.Background(), []string{"k1", "k2"})
	if err != nil || counts["completed"] != 1 || counts["needs_embedding"] != 1 {
		t.Fatalf("counts=%v err=%v", counts, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
