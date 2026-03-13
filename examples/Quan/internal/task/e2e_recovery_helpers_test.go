package task

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"
)

func createTaskViaService(t *testing.T, svc *Service, bizID string, maxRetry int) int64 {
	t.Helper()
	payload, err := MarshalPayload(SendCouponNoticePayload{
		ClaimID:  1001,
		CouponID: 2001,
		UserID:   3001,
	})
	if err != nil {
		t.Fatalf("marshal task payload failed: %v", err)
	}
	taskRec, err := svc.CreateTask(context.Background(), CreateTaskRequest{
		TaskType: TaskTypeSendCouponNotice,
		BizID:    bizID,
		Payload:  payload,
		MaxRetry: maxRetry,
	})
	if err != nil {
		t.Fatalf("create task via service failed: %v", err)
	}
	return taskRec.ID
}

func createTaskDirect(t *testing.T, repo *Repository, bizID string, maxRetry int) int64 {
	t.Helper()
	payload, err := MarshalPayload(SendCouponNoticePayload{
		ClaimID:  1001,
		CouponID: 2001,
		UserID:   3001,
	})
	if err != nil {
		t.Fatalf("marshal direct task payload failed: %v", err)
	}
	taskRec, err := repo.Create(context.Background(), CreateTaskParams{
		TaskType: TaskTypeSendCouponNotice,
		BizID:    bizID,
		Payload:  payload,
		MaxRetry: maxRetry,
	})
	if err != nil {
		t.Fatalf("create direct task failed: %v", err)
	}
	return taskRec.ID
}

func waitOutboxEventStatus(t *testing.T, db *sql.DB, taskID int64, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	aggregateID := strconv.FormatInt(taskID, 10)
	for time.Now().Before(deadline) {
		got, ok := queryOutboxEventStatus(t, db, aggregateID)
		if ok && got == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	got, ok := queryOutboxEventStatus(t, db, aggregateID)
	if !ok {
		t.Fatalf("outbox event not found for task_id=%d", taskID)
	}
	t.Fatalf("wait outbox status timeout: want=%s got=%s task_id=%d", want, got, taskID)
}

func queryOutboxEventStatus(t *testing.T, db *sql.DB, aggregateID string) (string, bool) {
	t.Helper()
	var status string
	err := db.QueryRow(`
SELECT status
FROM outbox_events
WHERE aggregate_type = 'async_task' AND aggregate_id = ?
ORDER BY event_id DESC
LIMIT 1
`, aggregateID).Scan(&status)
	if err != nil {
		return "", false
	}
	return status, true
}
