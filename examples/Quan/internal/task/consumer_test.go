package task

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeConsumerRepo struct {
	tryTask AsyncTask
	tryOK   bool
	tryErr  error

	markFailedDead bool
	markFailedNext *time.Time
	markFailedErr  error

	markSuccessErr error
	markSuspendErr error

	mu              sync.Mutex
	tryCalls        int
	markFailedCalls int
	markSuccessIDs  []int64
	markSuspendIDs  []int64
}

func (f *fakeConsumerRepo) TryMarkRunning(_ context.Context, taskID int64) (AsyncTask, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tryCalls++
	if f.tryTask.ID == 0 {
		f.tryTask.ID = taskID
	}
	return f.tryTask, f.tryOK, f.tryErr
}

func (f *fakeConsumerRepo) MarkFailed(_ context.Context, taskID int64, _ int64, _ string, _ time.Duration) (bool, *time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markFailedCalls++
	if f.tryTask.ID == 0 {
		f.tryTask.ID = taskID
	}
	return f.markFailedDead, f.markFailedNext, f.markFailedErr
}

func (f *fakeConsumerRepo) MarkSuccess(_ context.Context, taskID int64, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markSuccessIDs = append(f.markSuccessIDs, taskID)
	return f.markSuccessErr
}

func (f *fakeConsumerRepo) MarkSuspended(_ context.Context, taskID int64, _ int64, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markSuspendIDs = append(f.markSuspendIDs, taskID)
	return f.markSuspendErr
}

type fakeConsumerQueue struct {
	popTaskID int64
	popOK     bool
	popErr    error

	scheduleErr error
	pushDLQErr  error
	moveErr     error
	moveN       int

	mu               sync.Mutex
	scheduledTaskIDs []int64
	dlqTaskIDs       []int64
	moveCalls        int
}

func (f *fakeConsumerQueue) MoveDueRetryToReady(_ context.Context, _ int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.moveCalls++
	return f.moveN, f.moveErr
}

func (f *fakeConsumerQueue) PopReady(_ context.Context, _ time.Duration) (int64, bool, error) {
	return f.popTaskID, f.popOK, f.popErr
}

func (f *fakeConsumerQueue) PushDLQ(_ context.Context, taskID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dlqTaskIDs = append(f.dlqTaskIDs, taskID)
	return f.pushDLQErr
}

func (f *fakeConsumerQueue) ScheduleRetry(_ context.Context, taskID int64, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scheduledTaskIDs = append(f.scheduledTaskIDs, taskID)
	return f.scheduleErr
}

type fakeHandler struct {
	err error
}

func (h *fakeHandler) Handle(_ context.Context, _ AsyncTask) error {
	return h.err
}

func TestConsumeTaskSuccessMarksSuccess(t *testing.T) {
	repo := &fakeConsumerRepo{
		tryTask: AsyncTask{ID: 101, TaskType: TaskTypeSendCouponNotice},
		tryOK:   true,
	}
	queue := &fakeConsumerQueue{}
	registry := NewHandlerRegistry()
	registry.Register(TaskTypeSendCouponNotice, &fakeHandler{})

	c, err := NewConsumer(repo, queue, registry, ConsumeConfig{Enabled: true})
	if err != nil {
		t.Fatalf("new consumer failed: %v", err)
	}

	if err := c.consumeTask(context.Background(), 101, 1); err != nil {
		t.Fatalf("consume task failed: %v", err)
	}
	if len(repo.markSuccessIDs) != 1 || repo.markSuccessIDs[0] != 101 {
		t.Fatalf("expected mark success once for task 101, got %+v", repo.markSuccessIDs)
	}
	if repo.markFailedCalls != 0 {
		t.Fatalf("expected mark failed not called, got %d", repo.markFailedCalls)
	}
	if len(queue.scheduledTaskIDs) != 0 || len(queue.dlqTaskIDs) != 0 {
		t.Fatalf("expected no retry/dlq actions")
	}
}

func TestConsumeTaskFailureSchedulesRetry(t *testing.T) {
	next := time.Now().UTC().Add(2 * time.Second)
	repo := &fakeConsumerRepo{
		tryTask:        AsyncTask{ID: 202, TaskType: TaskTypeSendCouponNotice},
		tryOK:          true,
		markFailedDead: false,
		markFailedNext: &next,
	}
	queue := &fakeConsumerQueue{}
	registry := NewHandlerRegistry()
	registry.Register(TaskTypeSendCouponNotice, &fakeHandler{err: errors.New("handler failed")})

	c, err := NewConsumer(repo, queue, registry, ConsumeConfig{Enabled: true})
	if err != nil {
		t.Fatalf("new consumer failed: %v", err)
	}

	if err := c.consumeTask(context.Background(), 202, 1); err != nil {
		t.Fatalf("consume task failed: %v", err)
	}
	if repo.markFailedCalls != 1 {
		t.Fatalf("expected mark failed once, got %d", repo.markFailedCalls)
	}
	if len(queue.scheduledTaskIDs) != 1 || queue.scheduledTaskIDs[0] != 202 {
		t.Fatalf("expected schedule retry for task 202, got %+v", queue.scheduledTaskIDs)
	}
	if len(queue.dlqTaskIDs) != 0 {
		t.Fatalf("expected no dlq action, got %+v", queue.dlqTaskIDs)
	}
}

func TestConsumeTaskFailurePushesDLQ(t *testing.T) {
	repo := &fakeConsumerRepo{
		tryTask:        AsyncTask{ID: 303, TaskType: TaskTypeSendCouponNotice},
		tryOK:          true,
		markFailedDead: true,
	}
	queue := &fakeConsumerQueue{}
	registry := NewHandlerRegistry()
	registry.Register(TaskTypeSendCouponNotice, &fakeHandler{err: errors.New("handler failed")})

	c, err := NewConsumer(repo, queue, registry, ConsumeConfig{Enabled: true})
	if err != nil {
		t.Fatalf("new consumer failed: %v", err)
	}

	if err := c.consumeTask(context.Background(), 303, 1); err != nil {
		t.Fatalf("consume task failed: %v", err)
	}
	if len(queue.dlqTaskIDs) != 1 || queue.dlqTaskIDs[0] != 303 {
		t.Fatalf("expected dlq push for task 303, got %+v", queue.dlqTaskIDs)
	}
	if len(queue.scheduledTaskIDs) != 0 {
		t.Fatalf("expected no retry schedule, got %+v", queue.scheduledTaskIDs)
	}
}

func TestConsumeTaskNoopWhenTryMarkRunningMiss(t *testing.T) {
	repo := &fakeConsumerRepo{
		tryTask: AsyncTask{ID: 404, TaskType: TaskTypeSendCouponNotice},
		tryOK:   false,
	}
	queue := &fakeConsumerQueue{}
	registry := NewHandlerRegistry()
	registry.Register(TaskTypeSendCouponNotice, &fakeHandler{})

	c, err := NewConsumer(repo, queue, registry, ConsumeConfig{Enabled: true})
	if err != nil {
		t.Fatalf("new consumer failed: %v", err)
	}
	if err := c.consumeTask(context.Background(), 404, 1); err != nil {
		t.Fatalf("consume task failed: %v", err)
	}
	if repo.markFailedCalls != 0 || len(repo.markSuccessIDs) != 0 {
		t.Fatalf("expected no status transition, got failed=%d success=%d", repo.markFailedCalls, len(repo.markSuccessIDs))
	}
}

func TestRetrySchedulerCallsMoveDueRetryToReady(t *testing.T) {
	repo := &fakeConsumerRepo{}
	queue := &fakeConsumerQueue{moveN: 1}
	registry := NewHandlerRegistry()
	c, err := NewConsumer(repo, queue, registry, ConsumeConfig{
		Enabled:      true,
		PollInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new consumer failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.runRetryScheduler(ctx)
		close(done)
	}()
	time.Sleep(15 * time.Millisecond)
	cancel()
	<-done

	if queue.moveCalls == 0 {
		t.Fatalf("expected retry scheduler to call MoveDueRetryToReady")
	}
}

func TestConsumeTaskSuccessMarkSuccessFailureSuspendsTask(t *testing.T) {
	repo := &fakeConsumerRepo{
		tryTask:        AsyncTask{ID: 505, TaskType: TaskTypeSendCouponNotice},
		tryOK:          true,
		markSuccessErr: errors.New("db write failed"),
	}
	queue := &fakeConsumerQueue{}
	registry := NewHandlerRegistry()
	registry.Register(TaskTypeSendCouponNotice, &fakeHandler{})

	c, err := NewConsumer(repo, queue, registry, ConsumeConfig{Enabled: true})
	if err != nil {
		t.Fatalf("new consumer failed: %v", err)
	}

	if err := c.consumeTask(context.Background(), 505, 1); err == nil {
		t.Fatalf("expected consume task error when mark success fails")
	}
	if len(repo.markSuccessIDs) != 1 || repo.markSuccessIDs[0] != 505 {
		t.Fatalf("expected mark success once for task 505, got %+v", repo.markSuccessIDs)
	}
	if len(repo.markSuspendIDs) != 1 || repo.markSuspendIDs[0] != 505 {
		t.Fatalf("expected mark suspended once for task 505, got %+v", repo.markSuspendIDs)
	}
}

func TestConsumeTaskSuccessVersionConflictDoesNotSuspend(t *testing.T) {
	repo := &fakeConsumerRepo{
		tryTask:        AsyncTask{ID: 606, TaskType: TaskTypeSendCouponNotice, Version: 7},
		tryOK:          true,
		markSuccessErr: ErrTaskVersionConflict,
	}
	queue := &fakeConsumerQueue{}
	registry := NewHandlerRegistry()
	registry.Register(TaskTypeSendCouponNotice, &fakeHandler{})

	c, err := NewConsumer(repo, queue, registry, ConsumeConfig{Enabled: true})
	if err != nil {
		t.Fatalf("new consumer failed: %v", err)
	}

	if err := c.consumeTask(context.Background(), 606, 1); err != nil {
		t.Fatalf("expected version conflict to be treated as noop, got %v", err)
	}
	if len(repo.markSuspendIDs) != 0 {
		t.Fatalf("expected no suspend fallback on version conflict, got %+v", repo.markSuspendIDs)
	}
}
