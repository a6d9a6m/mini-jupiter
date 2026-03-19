package taskhandler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	taskmodel "mini-jupiter/examples/Quan/internal/task/model"

	mysqlerr "github.com/go-sql-driver/mysql"
)

type ConsumeReceiptRepository struct {
	db *sql.DB
}

func NewConsumeReceiptRepository(db *sql.DB) *ConsumeReceiptRepository {
	if db == nil {
		return nil
	}
	return &ConsumeReceiptRepository{db: db}
}

func (r *ConsumeReceiptRepository) TryCreate(ctx context.Context, task taskmodel.AsyncTask) (bool, error) {
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

func isDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	var mysqlErr *mysqlerr.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1062
	}
	return false
}
