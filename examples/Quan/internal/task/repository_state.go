package task

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func (r *Repository) TryMarkRunning(ctx context.Context, taskID int64) (AsyncTask, bool, error) {
	res, err := r.db.ExecContext(ctx, `
UPDATE async_tasks
SET status = ?, last_error = '', updated_at = ?
WHERE task_id = ? AND status IN (?, ?)
`, StatusRunning, time.Now().UTC(), taskID, StatusPending, StatusFailed)
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

func (r *Repository) MarkSuccess(ctx context.Context, taskID int64) error {
	_, err := r.db.ExecContext(ctx, `
UPDATE async_tasks
SET status = ?, last_error = '', next_retry_at = NULL, updated_at = ?
WHERE task_id = ? AND status = ?
`, StatusSuccess, time.Now().UTC(), taskID, StatusRunning)
	if err != nil {
		return fmt.Errorf("mark task success: %w", err)
	}
	return nil
}

func (r *Repository) MarkSuspended(ctx context.Context, taskID int64, lastErr string) error {
	_, err := r.db.ExecContext(ctx, `
UPDATE async_tasks
SET status = ?, last_error = ?, next_retry_at = NULL, updated_at = ?
WHERE task_id = ? AND status = ?
`, StatusSuspended, truncate(lastErr, 255), time.Now().UTC(), taskID, StatusRunning)
	if err != nil {
		return fmt.Errorf("mark task suspended: %w", err)
	}
	return nil
}

func (r *Repository) MarkReplayReady(ctx context.Context, taskID int64) error {
	res, err := r.db.ExecContext(ctx, `
UPDATE async_tasks
SET status = ?, next_retry_at = NULL, updated_at = ?
WHERE task_id = ? AND status IN (?, ?)
`, StatusFailed, time.Now().UTC(), taskID, StatusDead, StatusFailed)
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
SET status = ?, updated_at = ?
WHERE task_id = ? AND status = ?
`, StatusDead, time.Now().UTC(), taskID, StatusFailed)
	if err != nil {
		return fmt.Errorf("restore dead task after replay failure: %w", err)
	}
	return nil
}

func (r *Repository) MarkFailed(ctx context.Context, taskID int64, lastErr string, backoffBase time.Duration) (bool, *time.Time, error) {
	var (
		dead      bool
		nextRetry *time.Time
	)
	err := r.txm.WithinTx(ctx, nil, func(ctx context.Context, tx *sql.Tx) error {
		var (
			status     string
			retryCount int
			maxRetry   int
		)
		err := tx.QueryRowContext(ctx, `
SELECT status, retry_count, max_retry
FROM async_tasks
WHERE task_id = ?
FOR UPDATE
`, taskID).Scan(&status, &retryCount, &maxRetry)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrTaskNotFound
			}
			return fmt.Errorf("query task for retry: %w", err)
		}
		if status != StatusRunning {
			return nil
		}
		retryCount++
		now := time.Now().UTC()
		if retryCount >= maxRetry {
			dead = true
			_, err = tx.ExecContext(ctx, `
UPDATE async_tasks
SET status = ?, retry_count = ?, next_retry_at = NULL, last_error = ?, updated_at = ?
WHERE task_id = ?
`, StatusDead, retryCount, truncate(lastErr, 255), now, taskID)
			if err != nil {
				return fmt.Errorf("mark task dead: %w", err)
			}
			return nil
		}

		delay := taskBackoff(retryCount, backoffBase)
		next := now.Add(delay)
		nextRetry = &next
		_, err = tx.ExecContext(ctx, `
UPDATE async_tasks
SET status = ?, retry_count = ?, next_retry_at = ?, last_error = ?, updated_at = ?
WHERE task_id = ?
`, StatusFailed, retryCount, next, truncate(lastErr, 255), now, taskID)
		if err != nil {
			return fmt.Errorf("mark task retry: %w", err)
		}
		return nil
	})
	if err != nil {
		return false, nil, err
	}
	return dead, nextRetry, nil
}

func (r *Repository) MarkRecoveredForRetry(ctx context.Context, taskID int64, lastErr string) (bool, error) {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
UPDATE async_tasks
SET status = ?, next_retry_at = ?, last_error = ?, updated_at = ?
WHERE task_id = ? AND status IN (?, ?)
`, StatusFailed, now, truncate(lastErr, 255), now, taskID, StatusRunning, StatusSuspended)
	if err != nil {
		return false, fmt.Errorf("mark task recovered for retry: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("mark task recovered for retry rows affected: %w", err)
	}
	return affected > 0, nil
}
