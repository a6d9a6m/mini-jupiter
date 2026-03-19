package taskrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	taskmodel "mini-jupiter/examples/Quan/internal/task/model"
	"mini-jupiter/pkg/mysql"

	mysqlerr "github.com/go-sql-driver/mysql"
)

var (
	ErrTaskDuplicate       = errors.New("task duplicate")
	ErrTaskNotFound        = errors.New("task not found")
	ErrTaskNotReplayable   = errors.New("task not replayable")
	ErrTaskVersionConflict = errors.New("task version conflict")
)

type Repository struct {
	db  *sql.DB
	txm *mysql.TxManager
}

func NewRepository(db *sql.DB, txm *mysql.TxManager) *Repository {
	return &Repository{db: db, txm: txm}
}

func (r *Repository) CreateTx(ctx context.Context, tx *sql.Tx, p taskmodel.CreateTaskParams) (taskmodel.AsyncTask, error) {
	if p.MaxRetry <= 0 {
		p.MaxRetry = 5
	}
	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
INSERT INTO async_tasks
	(task_type, biz_id, status, payload_json, retry_count, max_retry, next_retry_at, last_error, version, created_at, updated_at)
VALUES
	(?, ?, ?, ?, 0, ?, NULL, '', 0, ?, ?)
`, p.TaskType, p.BizID, taskmodel.StatusPending, p.Payload, p.MaxRetry, now, now)
	if err != nil {
		if isDuplicateKey(err) {
			return taskmodel.AsyncTask{}, ErrTaskDuplicate
		}
		return taskmodel.AsyncTask{}, fmt.Errorf("insert async task: %w", err)
	}
	taskID, err := res.LastInsertId()
	if err != nil {
		return taskmodel.AsyncTask{}, fmt.Errorf("async task last insert id: %w", err)
	}
	return taskmodel.AsyncTask{
		ID:         taskID,
		TaskType:   p.TaskType,
		BizID:      p.BizID,
		Status:     taskmodel.StatusPending,
		Payload:    p.Payload,
		RetryCount: 0,
		MaxRetry:   p.MaxRetry,
		Version:    0,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

func (r *Repository) Create(ctx context.Context, p taskmodel.CreateTaskParams) (taskmodel.AsyncTask, error) {
	var task taskmodel.AsyncTask
	err := r.txm.WithinTx(ctx, nil, func(ctx context.Context, tx *sql.Tx) error {
		created, createErr := r.CreateTx(ctx, tx, p)
		if createErr != nil {
			return createErr
		}
		task = created
		return nil
	})
	if err != nil {
		return taskmodel.AsyncTask{}, err
	}
	return task, nil
}

type taskRow interface {
	Scan(dest ...any) error
}

func (r *Repository) GetByID(ctx context.Context, taskID int64) (taskmodel.AsyncTask, error) {
	return scanTask(
		r.db.QueryRowContext(ctx, `
SELECT task_id, task_type, biz_id, status, payload_json, retry_count, max_retry, next_retry_at, last_error, version, created_at, updated_at
FROM async_tasks
WHERE task_id = ?
LIMIT 1
`, taskID),
	)
}

func (r *Repository) GetByTypeBiz(ctx context.Context, taskType, bizID string) (taskmodel.AsyncTask, error) {
	return scanTask(
		r.db.QueryRowContext(ctx, `
SELECT task_id, task_type, biz_id, status, payload_json, retry_count, max_retry, next_retry_at, last_error, version, created_at, updated_at
FROM async_tasks
WHERE task_type = ? AND biz_id = ?
LIMIT 1
`, taskType, bizID),
	)
}

func (r *Repository) ListDueFailedForCompensation(ctx context.Context, limit int) ([]taskmodel.RecoveryCandidate, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT task_id, next_retry_at, version
FROM async_tasks
WHERE status = ? AND next_retry_at IS NOT NULL AND next_retry_at <= ?
ORDER BY next_retry_at ASC
LIMIT ?
`, taskmodel.StatusFailed, time.Now().UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list due failed tasks for compensation: %w", err)
	}
	defer rows.Close()

	return scanRecoveryCandidates(rows, limit, taskmodel.RecoverySourceRetryDue, "due failed")
}

func (r *Repository) ListSuspendedForCompensation(ctx context.Context, staleBefore time.Time, limit int) ([]taskmodel.RecoveryCandidate, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT task_id, updated_at, version
FROM async_tasks
WHERE status = ? AND updated_at <= ?
ORDER BY updated_at ASC
LIMIT ?
`, taskmodel.StatusSuspended, staleBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("list suspended tasks for compensation: %w", err)
	}
	defer rows.Close()

	return scanRecoveryCandidates(rows, limit, taskmodel.RecoverySourceSuspended, "suspended")
}

func (r *Repository) ListStaleRunningForCompensation(ctx context.Context, staleBefore time.Time, limit int) ([]taskmodel.RecoveryCandidate, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT task_id, updated_at, version
FROM async_tasks
WHERE status = ? AND updated_at <= ?
ORDER BY updated_at ASC
LIMIT ?
`, taskmodel.StatusRunning, staleBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("list stale running tasks for compensation: %w", err)
	}
	defer rows.Close()

	return scanRecoveryCandidates(rows, limit, taskmodel.RecoverySourceStaleRunning, "stale running")
}

func scanRecoveryCandidates(rows *sql.Rows, limit int, source string, label string) ([]taskmodel.RecoveryCandidate, error) {
	result := make([]taskmodel.RecoveryCandidate, 0, limit)
	for rows.Next() {
		var candidate taskmodel.RecoveryCandidate
		if scanErr := rows.Scan(&candidate.TaskID, &candidate.RecoverAt, &candidate.Version); scanErr != nil {
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

func scanTask(row taskRow) (taskmodel.AsyncTask, error) {
	var (
		t      taskmodel.AsyncTask
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
		&t.Version,
		&t.CreatedAt,
		&t.UpdatedAt,
	)
	if err != nil {
		return taskmodel.AsyncTask{}, err
	}
	if nextAt.Valid {
		next := nextAt.Time
		t.NextRetry = &next
	}
	return t, nil
}

func (r *Repository) TryMarkRunning(ctx context.Context, taskID int64) (taskmodel.AsyncTask, bool, error) {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
UPDATE async_tasks
SET status = ?, last_error = '', version = version + 1, updated_at = ?
WHERE task_id = ? AND status IN (?, ?)
`, taskmodel.StatusRunning, now, taskID, taskmodel.StatusPending, taskmodel.StatusFailed)
	if err != nil {
		return taskmodel.AsyncTask{}, false, fmt.Errorf("mark task running: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return taskmodel.AsyncTask{}, false, fmt.Errorf("mark task running rows affected: %w", err)
	}
	if affected == 0 {
		return taskmodel.AsyncTask{}, false, nil
	}
	task, err := r.GetByID(ctx, taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return taskmodel.AsyncTask{}, false, ErrTaskNotFound
		}
		return taskmodel.AsyncTask{}, false, err
	}
	return task, true, nil
}

func (r *Repository) MarkSuccess(ctx context.Context, taskID int64, expectedVersion int64) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
UPDATE async_tasks
SET status = ?, last_error = '', next_retry_at = NULL, version = version + 1, updated_at = ?
WHERE task_id = ? AND status = ? AND version = ?
`, taskmodel.StatusSuccess, now, taskID, taskmodel.StatusRunning, expectedVersion)
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
`, taskmodel.StatusSuspended, truncate(lastErr, 255), now, taskID, taskmodel.StatusRunning, expectedVersion)
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
`, taskmodel.StatusFailed, now, taskID, taskmodel.StatusDead, taskmodel.StatusFailed)
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
`, taskmodel.StatusDead, time.Now().UTC(), taskID, taskmodel.StatusFailed)
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
	if status != taskmodel.StatusRunning {
		return false, nil, ErrTaskVersionConflict
	}

	retryCount++
	now := time.Now().UTC()
	if retryCount >= maxRetry {
		res, execErr := r.db.ExecContext(ctx, `
UPDATE async_tasks
SET status = ?, retry_count = ?, next_retry_at = NULL, last_error = ?, version = version + 1, updated_at = ?
WHERE task_id = ? AND status = ? AND version = ?
`, taskmodel.StatusDead, retryCount, truncate(lastErr, 255), now, taskID, taskmodel.StatusRunning, expectedVersion)
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
`, taskmodel.StatusFailed, retryCount, next, truncate(lastErr, 255), now, taskID, taskmodel.StatusRunning, expectedVersion)
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
`, taskmodel.StatusFailed, now, truncate(lastErr, 255), now, taskID, taskmodel.StatusRunning, taskmodel.StatusSuspended, expectedVersion)
	if err != nil {
		return false, fmt.Errorf("mark task recovered for retry: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("mark task recovered for retry rows affected: %w", err)
	}
	return affected > 0, nil
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
