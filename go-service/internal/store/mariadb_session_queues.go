package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// SessionQueueCounts is the background work still open for one session, by
// status: vector operations and memory reprocessing jobs.
type SessionQueueCounts struct {
	Vector       map[string]int `json:"vector"`
	Reprocessing map[string]int `json:"reprocessing"`
}

// SessionQueueStore reads and clears a session's open background work.
type SessionQueueStore interface {
	CountSessionQueues(ctx context.Context, chatSessionID string) (SessionQueueCounts, error)
	// WithdrawSessionQueues rejects the session's work no worker has started.
	WithdrawSessionQueues(ctx context.Context, chatSessionID, reason string) (vector, reprocessing int, err error)
	// MemoryReprocessingStatesForTurns is the latest reprocessing job status
	// of each turn's active source revision; turns without one are absent.
	MemoryReprocessingStatesForTurns(ctx context.Context, chatSessionID string, turns []int) (map[int]string, error)
}

func (m *mariadbStore) CountSessionQueues(ctx context.Context, chatSessionID string) (SessionQueueCounts, error) {
	counts := SessionQueueCounts{Vector: map[string]int{}, Reprocessing: map[string]int{}}
	if err := m.ensureDB(); err != nil {
		return counts, err
	}
	for _, query := range []struct {
		sql string
		out map[string]int
	}{
		{`SELECT status, COUNT(*) FROM memory_vector_outbox
		  WHERE chat_session_id = ? AND status IN ('pending', 'leased', 'retryable', 'needs_embedding')
		  GROUP BY status`, counts.Vector},
		{`SELECT status, COUNT(*) FROM memory_reprocessing_jobs
		  WHERE chat_session_id = ? AND status IN ('pending', 'leased', 'retryable')
		  GROUP BY status`, counts.Reprocessing},
	} {
		if err := scanStatusCounts(ctx, m.db, query.sql, query.out, chatSessionID); err != nil {
			return counts, err
		}
	}
	return counts, nil
}

func scanStatusCounts(ctx context.Context, db *sql.DB, query string, out map[string]int, args ...any) error {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return err
		}
		out[status] += count
	}
	return rows.Err()
}

func (m *mariadbStore) WithdrawSessionQueues(ctx context.Context, chatSessionID, reason string) (int, int, error) {
	if err := m.ensureDB(); err != nil {
		return 0, 0, err
	}
	now := time.Now().UTC()
	vector, err := m.db.ExecContext(ctx, `
		UPDATE memory_vector_outbox
		SET status = 'stale_rejected', retry_after = NULL,
		    lease_owner = NULL, lease_until = NULL, last_error = ?, updated_at = ?
		WHERE chat_session_id = ? AND status IN ('pending', 'retryable', 'needs_embedding')
	`, reason, now, chatSessionID)
	if err != nil {
		return 0, 0, err
	}
	vectorCount, err := vector.RowsAffected()
	if err != nil {
		return 0, 0, err
	}
	reprocessing, err := m.db.ExecContext(ctx, `
		UPDATE memory_reprocessing_jobs
		SET status = 'stale_rejected', retry_after = NULL,
		    lease_owner = NULL, lease_until = NULL, last_error = ?, updated_at = ?
		WHERE chat_session_id = ? AND status IN ('pending', 'retryable')
	`, reason, now, chatSessionID)
	if err != nil {
		return int(vectorCount), 0, err
	}
	reprocessingCount, err := reprocessing.RowsAffected()
	if err != nil {
		return int(vectorCount), 0, err
	}
	return int(vectorCount), int(reprocessingCount), nil
}

func (m *mariadbStore) MemoryReprocessingStatesForTurns(ctx context.Context, chatSessionID string, turns []int) (map[int]string, error) {
	states := map[int]string{}
	if err := m.ensureDB(); err != nil {
		return states, err
	}
	if len(turns) == 0 {
		return states, nil
	}
	args := []any{chatSessionID}
	for _, turn := range turns {
		args = append(args, turn)
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT s.turn_index, j.status
		FROM memory_source_revisions s
		JOIN memory_reprocessing_jobs j
		  ON j.chat_session_id = s.chat_session_id AND j.source_revision = s.source_revision
		WHERE s.chat_session_id = ? AND s.lifecycle_state = 'active'
		  AND s.turn_index IN (?`+strings.Repeat(",?", len(turns)-1)+`)
		ORDER BY j.id
	`, args...)
	if err != nil {
		return states, err
	}
	defer rows.Close()
	for rows.Next() {
		var turn int
		var status string
		if err := rows.Scan(&turn, &status); err != nil {
			return states, err
		}
		states[turn] = status
	}
	return states, rows.Err()
}

func (d *dualWriteStore) sessionQueueStore() (SessionQueueStore, bool) {
	if primary, ok := d.primary.(SessionQueueStore); ok {
		return primary, true
	}
	shadow, ok := d.shadow.(SessionQueueStore)
	return shadow, ok
}

func (d *dualWriteStore) CountSessionQueues(ctx context.Context, chatSessionID string) (SessionQueueCounts, error) {
	if queues, ok := d.sessionQueueStore(); ok {
		return queues.CountSessionQueues(ctx, chatSessionID)
	}
	return SessionQueueCounts{}, ErrNotEnabled
}

func (d *dualWriteStore) WithdrawSessionQueues(ctx context.Context, chatSessionID, reason string) (int, int, error) {
	if queues, ok := d.sessionQueueStore(); ok {
		return queues.WithdrawSessionQueues(ctx, chatSessionID, reason)
	}
	return 0, 0, ErrNotEnabled
}

func (d *dualWriteStore) MemoryReprocessingStatesForTurns(ctx context.Context, chatSessionID string, turns []int) (map[int]string, error) {
	if queues, ok := d.sessionQueueStore(); ok {
		return queues.MemoryReprocessingStatesForTurns(ctx, chatSessionID, turns)
	}
	return nil, ErrNotEnabled
}
