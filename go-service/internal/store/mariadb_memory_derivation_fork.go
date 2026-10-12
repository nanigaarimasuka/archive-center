package store

import (
	"context"
	"strings"
	"time"
)

// WithdrawMemoryVectorOperations marks the given operations rejected while
// they still wait; one a worker holds or finished is left as it is.
func (m *mariadbStore) WithdrawMemoryVectorOperations(ctx context.Context, operationKeys []string, reason string) (int, error) {
	if err := m.ensureDB(); err != nil {
		return 0, err
	}
	withdrawn := 0
	const batch = 500
	for start := 0; start < len(operationKeys); start += batch {
		keys := operationKeys[start:min(start+batch, len(operationKeys))]
		args := []any{reason, time.Now().UTC()}
		for _, key := range keys {
			args = append(args, key)
		}
		result, err := m.db.ExecContext(ctx, `
			UPDATE memory_vector_outbox
			SET status = 'stale_rejected', retry_after = NULL,
			    lease_owner = NULL, lease_until = NULL, last_error = ?, updated_at = ?
			WHERE operation_key IN (?`+strings.Repeat(",?", len(keys)-1)+`)
			  AND status IN ('pending', 'retryable', 'needs_embedding')
		`, args...)
		if err != nil {
			return withdrawn, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return withdrawn, err
		}
		withdrawn += int(affected)
	}
	return withdrawn, nil
}

func (m *mariadbStore) CountMemoryVectorOperationStates(ctx context.Context, operationKeys []string) (map[string]int, error) {
	if err := m.ensureDB(); err != nil {
		return nil, err
	}
	counts := map[string]int{}
	const batch = 500
	for start := 0; start < len(operationKeys); start += batch {
		keys := operationKeys[start:min(start+batch, len(operationKeys))]
		args := make([]any, 0, len(keys))
		for _, key := range keys {
			args = append(args, key)
		}
		rows, err := m.db.QueryContext(ctx, `
			SELECT status, COUNT(*)
			FROM memory_vector_outbox
			WHERE operation_key IN (?`+strings.Repeat(",?", len(keys)-1)+`)
			GROUP BY status
		`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var status string
			var count int
			if err := rows.Scan(&status, &count); err != nil {
				_ = rows.Close()
				return nil, err
			}
			counts[status] += count
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return counts, nil
}
