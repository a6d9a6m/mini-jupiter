package task

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

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
