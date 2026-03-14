package task

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type consumeReceiptStore interface {
	TryCreate(ctx context.Context, task AsyncTask) (bool, error)
}

type noopConsumeReceiptStore struct{}

func (noopConsumeReceiptStore) TryCreate(context.Context, AsyncTask) (bool, error) {
	return true, nil
}

type ConsumeReceiptRepository struct {
	db *sql.DB
}

func NewConsumeReceiptRepository(db *sql.DB) *ConsumeReceiptRepository {
	if db == nil {
		return nil
	}
	return &ConsumeReceiptRepository{db: db}
}

func (r *ConsumeReceiptRepository) TryCreate(ctx context.Context, task AsyncTask) (bool, error) {
	if r == nil || r.db == nil {
		return true, nil
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO task_consume_receipts
	(task_id, task_type, biz_id, created_at)
VALUES
	(?, ?, ?, ?)
`, task.ID, task.TaskType, task.BizID, time.Now().UTC())
	if err != nil {
		if isDuplicateKey(err) {
			return false, nil
		}
		return false, fmt.Errorf("insert task consume receipt: %w", err)
	}
	return true, nil
}
