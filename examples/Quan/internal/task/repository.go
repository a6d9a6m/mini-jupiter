package task

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"mini-jupiter/pkg/mysql"

	mysqlerr "github.com/go-sql-driver/mysql"
)

var (
	ErrTaskDuplicate     = errors.New("task duplicate")
	ErrTaskNotFound      = errors.New("task not found")
	ErrTaskNotReplayable = errors.New("task not replayable")
)

type Repository struct {
	db  *sql.DB
	txm *mysql.TxManager
}

func NewRepository(db *sql.DB, txm *mysql.TxManager) *Repository {
	return &Repository{db: db, txm: txm}
}

func (r *Repository) CreateTx(ctx context.Context, tx *sql.Tx, p CreateTaskParams) (AsyncTask, error) {
	if p.MaxRetry <= 0 {
		p.MaxRetry = 5
	}
	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
INSERT INTO async_tasks
	(task_type, biz_id, status, payload_json, retry_count, max_retry, next_retry_at, last_error, created_at, updated_at)
VALUES
	(?, ?, ?, ?, 0, ?, NULL, '', ?, ?)
`, p.TaskType, p.BizID, StatusPending, p.Payload, p.MaxRetry, now, now)
	if err != nil {
		if isDuplicateKey(err) {
			return AsyncTask{}, ErrTaskDuplicate
		}
		return AsyncTask{}, fmt.Errorf("insert async task: %w", err)
	}
	taskID, err := res.LastInsertId()
	if err != nil {
		return AsyncTask{}, fmt.Errorf("async task last insert id: %w", err)
	}
	return AsyncTask{
		ID:         taskID,
		TaskType:   p.TaskType,
		BizID:      p.BizID,
		Status:     StatusPending,
		Payload:    p.Payload,
		RetryCount: 0,
		MaxRetry:   p.MaxRetry,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

func (r *Repository) Create(ctx context.Context, p CreateTaskParams) (AsyncTask, error) {
	var task AsyncTask
	err := r.txm.WithinTx(ctx, nil, func(ctx context.Context, tx *sql.Tx) error {
		created, createErr := r.CreateTx(ctx, tx, p)
		if createErr != nil {
			return createErr
		}
		task = created
		return nil
	})
	if err != nil {
		return AsyncTask{}, err
	}
	return task, nil
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

	result := make([]RecoveryCandidate, 0, limit)
	for rows.Next() {
		var candidate RecoveryCandidate
		if scanErr := rows.Scan(&candidate.TaskID, &candidate.RecoverAt); scanErr != nil {
			return nil, fmt.Errorf("scan due failed task id: %w", scanErr)
		}
		candidate.Source = RecoverySourceRetryDue
		result = append(result, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due failed tasks: %w", err)
	}
	return result, nil
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

	result := make([]RecoveryCandidate, 0, limit)
	for rows.Next() {
		var candidate RecoveryCandidate
		if scanErr := rows.Scan(&candidate.TaskID, &candidate.RecoverAt); scanErr != nil {
			return nil, fmt.Errorf("scan suspended task id: %w", scanErr)
		}
		candidate.Source = RecoverySourceSuspended
		result = append(result, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate suspended tasks: %w", err)
	}
	return result, nil
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

	result := make([]RecoveryCandidate, 0, limit)
	for rows.Next() {
		var candidate RecoveryCandidate
		if scanErr := rows.Scan(&candidate.TaskID, &candidate.RecoverAt); scanErr != nil {
			return nil, fmt.Errorf("scan stale running task id: %w", scanErr)
		}
		candidate.Source = RecoverySourceStaleRunning
		result = append(result, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stale running tasks: %w", err)
	}
	return result, nil
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

type taskRow interface {
	Scan(dest ...any) error
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

func isDuplicateKey(err error) bool {
	var me *mysqlerr.MySQLError
	if errors.As(err, &me) {
		return me.Number == 1062
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func taskBackoff(retryCount int, base time.Duration) time.Duration {
	if base <= 0 {
		base = 1 * time.Second
	}
	if retryCount < 0 {
		retryCount = 0
	}
	if retryCount > 8 {
		retryCount = 8
	}
	return base * time.Duration(1<<retryCount)
}
