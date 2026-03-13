package task

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type taskRow interface {
	Scan(dest ...any) error
}

func (r *Repository) GetByID(ctx context.Context, taskID int64) (AsyncTask, error) {
	return scanTask(
		r.db.QueryRowContext(ctx, `
SELECT task_id, task_type, biz_id, status, payload_json, retry_count, max_retry, next_retry_at, last_error, created_at, updated_at
FROM async_tasks
WHERE task_id = ?
LIMIT 1
`, taskID),
	)
}

func (r *Repository) GetByTypeBiz(ctx context.Context, taskType, bizID string) (AsyncTask, error) {
	return scanTask(
		r.db.QueryRowContext(ctx, `
SELECT task_id, task_type, biz_id, status, payload_json, retry_count, max_retry, next_retry_at, last_error, created_at, updated_at
FROM async_tasks
WHERE task_type = ? AND biz_id = ?
LIMIT 1
`, taskType, bizID),
	)
}

func (r *Repository) ListDueFailedForCompensation(ctx context.Context, limit int) ([]RecoveryCandidate, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT task_id, next_retry_at
FROM async_tasks
WHERE status = ? AND next_retry_at IS NOT NULL AND next_retry_at <= ?
ORDER BY next_retry_at ASC
LIMIT ?
`, StatusFailed, time.Now().UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list due failed tasks for compensation: %w", err)
	}
	defer rows.Close()

	return scanRecoveryCandidates(rows, limit, RecoverySourceRetryDue, "due failed")
}

func (r *Repository) ListSuspendedForCompensation(ctx context.Context, staleBefore time.Time, limit int) ([]RecoveryCandidate, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT task_id, updated_at
FROM async_tasks
WHERE status = ? AND updated_at <= ?
ORDER BY updated_at ASC
LIMIT ?
`, StatusSuspended, staleBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("list suspended tasks for compensation: %w", err)
	}
	defer rows.Close()

	return scanRecoveryCandidates(rows, limit, RecoverySourceSuspended, "suspended")
}

func (r *Repository) ListStaleRunningForCompensation(ctx context.Context, staleBefore time.Time, limit int) ([]RecoveryCandidate, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT task_id, updated_at
FROM async_tasks
WHERE status = ? AND updated_at <= ?
ORDER BY updated_at ASC
LIMIT ?
`, StatusRunning, staleBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("list stale running tasks for compensation: %w", err)
	}
	defer rows.Close()

	return scanRecoveryCandidates(rows, limit, RecoverySourceStaleRunning, "stale running")
}

func scanRecoveryCandidates(rows *sql.Rows, limit int, source string, label string) ([]RecoveryCandidate, error) {
	result := make([]RecoveryCandidate, 0, limit)
	for rows.Next() {
		var candidate RecoveryCandidate
		if scanErr := rows.Scan(&candidate.TaskID, &candidate.RecoverAt); scanErr != nil {
			return nil, fmt.Errorf("scan %s task id: %w", label, scanErr)
		}
		candidate.Source = source
		result = append(result, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s tasks: %w", label, err)
	}
	return result, nil
}

func scanTask(row taskRow) (AsyncTask, error) {
	var (
		t      AsyncTask
		nextAt sql.NullTime
	)
	err := row.Scan(
		&t.ID,
		&t.TaskType,
		&t.BizID,
		&t.Status,
		&t.Payload,
		&t.RetryCount,
		&t.MaxRetry,
		&nextAt,
		&t.LastError,
		&t.CreatedAt,
		&t.UpdatedAt,
	)
	if err != nil {
		return AsyncTask{}, err
	}
	if nextAt.Valid {
		next := nextAt.Time
		t.NextRetry = &next
	}
	return t, nil
}
