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
	ErrTaskDuplicate = errors.New("task duplicate")
	ErrTaskNotFound  = errors.New("task not found")
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
