package task

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func (r *Repository) TryMarkRunning(ctx context.Context, taskID int64) (AsyncTask, bool, error) {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
UPDATE async_tasks
SET status = ?, last_error = '', version = version + 1, updated_at = ?
WHERE task_id = ? AND status IN (?, ?)
`, StatusRunning, now, taskID, StatusPending, StatusFailed)
	if err != nil {
		return AsyncTask{}, false, fmt.Errorf("mark task running: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return AsyncTask{}, false, fmt.Errorf("mark task running rows affected: %w", err)
	}
	if affected == 0 {
		return AsyncTask{}, false, nil
	}
	task, err := r.GetByID(ctx, taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AsyncTask{}, false, ErrTaskNotFound
		}
		return AsyncTask{}, false, err
	}
	return task, true, nil
}

func (r *Repository) MarkSuccess(ctx context.Context, taskID int64, expectedVersion int64) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
UPDATE async_tasks
SET status = ?, last_error = '', next_retry_at = NULL, version = version + 1, updated_at = ?
WHERE task_id = ? AND status = ? AND version = ?
`, StatusSuccess, now, taskID, StatusRunning, expectedVersion)
	if err != nil {
		return fmt.Errorf("mark task success: %w", err)
	}
	return taskCASResult(res, "mark task success")
}

func (r *Repository) MarkSuspended(ctx context.Context, taskID int64, expectedVersion int64, lastErr string) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
UPDATE async_tasks
SET status = ?, last_error = ?, next_retry_at = NULL, version = version + 1, updated_at = ?
WHERE task_id = ? AND status = ? AND version = ?
`, StatusSuspended, truncate(lastErr, 255), now, taskID, StatusRunning, expectedVersion)
	if err != nil {
		return fmt.Errorf("mark task suspended: %w", err)
	}
	return taskCASResult(res, "mark task suspended")
}

func (r *Repository) MarkReplayReady(ctx context.Context, taskID int64) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
UPDATE async_tasks
SET status = ?, next_retry_at = NULL, version = version + 1, updated_at = ?
WHERE task_id = ? AND status IN (?, ?)
`, StatusFailed, now, taskID, StatusDead, StatusFailed)
	if err != nil {
		return fmt.Errorf("mark task replay ready: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark task replay ready rows affected: %w", err)
	}
	if affected > 0 {
		return nil
	}

	var status string
	err = r.db.QueryRowContext(ctx, `SELECT status FROM async_tasks WHERE task_id = ? LIMIT 1`, taskID).Scan(&status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTaskNotFound
		}
		return fmt.Errorf("query task replay status: %w", err)
	}
	return fmt.Errorf("%w: status=%s", ErrTaskNotReplayable, status)
}

func (r *Repository) RestoreDeadAfterReplayFailure(ctx context.Context, taskID int64) error {
	_, err := r.db.ExecContext(ctx, `
UPDATE async_tasks
SET status = ?, version = version + 1, updated_at = ?
WHERE task_id = ? AND status = ?
`, StatusDead, time.Now().UTC(), taskID, StatusFailed)
	if err != nil {
		return fmt.Errorf("restore dead task after replay failure: %w", err)
	}
	return nil
}

func (r *Repository) MarkFailed(ctx context.Context, taskID int64, expectedVersion int64, lastErr string, backoffBase time.Duration) (bool, *time.Time, error) {
	var (
		status     string
		retryCount int
		maxRetry   int
	)
	err := r.db.QueryRowContext(ctx, `
SELECT status, retry_count, max_retry
FROM async_tasks
WHERE task_id = ? AND version = ?
LIMIT 1
`, taskID, expectedVersion).Scan(&status, &retryCount, &maxRetry)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil, ErrTaskVersionConflict
		}
		return false, nil, fmt.Errorf("query task for retry: %w", err)
	}
	if status != StatusRunning {
		return false, nil, ErrTaskVersionConflict
	}

	retryCount++
	now := time.Now().UTC()
	if retryCount >= maxRetry {
		res, execErr := r.db.ExecContext(ctx, `
UPDATE async_tasks
SET status = ?, retry_count = ?, next_retry_at = NULL, last_error = ?, version = version + 1, updated_at = ?
WHERE task_id = ? AND status = ? AND version = ?
`, StatusDead, retryCount, truncate(lastErr, 255), now, taskID, StatusRunning, expectedVersion)
		if execErr != nil {
			return false, nil, fmt.Errorf("mark task dead: %w", execErr)
		}
		if err := taskCASResult(res, "mark task dead"); err != nil {
			return false, nil, err
		}
		return true, nil, nil
	}

	delay := taskBackoff(retryCount, backoffBase)
	next := now.Add(delay)
	res, execErr := r.db.ExecContext(ctx, `
UPDATE async_tasks
SET status = ?, retry_count = ?, next_retry_at = ?, last_error = ?, version = version + 1, updated_at = ?
WHERE task_id = ? AND status = ? AND version = ?
`, StatusFailed, retryCount, next, truncate(lastErr, 255), now, taskID, StatusRunning, expectedVersion)
	if execErr != nil {
		return false, nil, fmt.Errorf("mark task retry: %w", execErr)
	}
	if err := taskCASResult(res, "mark task retry"); err != nil {
		return false, nil, err
	}
	return false, &next, nil
}

func (r *Repository) MarkRecoveredForRetry(ctx context.Context, taskID int64, expectedVersion int64, lastErr string) (bool, error) {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
UPDATE async_tasks
SET status = ?, next_retry_at = ?, last_error = ?, version = version + 1, updated_at = ?
WHERE task_id = ? AND status IN (?, ?) AND version = ?
`, StatusFailed, now, truncate(lastErr, 255), now, taskID, StatusRunning, StatusSuspended, expectedVersion)
	if err != nil {
		return false, fmt.Errorf("mark task recovered for retry: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("mark task recovered for retry rows affected: %w", err)
	}
	return affected > 0, nil
}

func taskCASResult(res sql.Result, action string) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s rows affected: %w", action, err)
	}
	if affected == 0 {
		return ErrTaskVersionConflict
	}
	return nil
}
