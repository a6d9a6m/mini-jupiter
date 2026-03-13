package task

import (
	"context"
	"testing"
	"time"

	"mini-jupiter/examples/Quan/internal/outbox"
	"mini-jupiter/pkg/mysql"
)

func TestE2E_TaskPipeline_RetryThenSuccess_NoSilentLoss(t *testing.T) {
	db := openTaskIntegrationDB(t)
	redisClient := openTaskIntegrationRedis(t)
	ctx := context.Background()

	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	outboxRepo := outbox.NewRepository(db)
	taskRepo := NewRepository(db, txm)
	queue, err := NewQueue(redisClient, QueueConfig{})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}

	handler := &flakyTaskHandler{failTimes: 1}
	registry := NewHandlerRegistry()
	registry.Register(TaskTypeSendCouponNotice, handler)

	relay, err := outbox.NewRelay(outboxRepo, queue, outbox.RelayConfig{
		Enabled:      true,
		PollInterval: 20 * time.Millisecond,
		BatchSize:    100,
		BackoffBase:  20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new relay failed: %v", err)
	}
	consumer, err := NewConsumer(taskRepo, queue, registry, ConsumeConfig{
		Enabled:         true,
		Workers:         1,
		PollInterval:    20 * time.Millisecond,
		ReadyTimeout:    1 * time.Second,
		RetryBackoff:    20 * time.Millisecond,
		RetryMoveBatch:  100,
		DefaultMaxRetry: 3,
	})
	if err != nil {
		t.Fatalf("new consumer failed: %v", err)
	}

	startBackgroundComponents(t, ctx, relay, consumer)
	taskService := NewServiceWithQueue(txm, taskRepo, outboxRepo, queue, 3)
	server := newTaskHTTPServer(taskService)
	defer server.Close()

	taskID := createTaskViaHTTP(t, server.URL, "e2e_retry_success_"+nextTaskTestSuffix(), 3)
	rec := waitTaskStatus(t, taskRepo, taskID, StatusSuccess, 8*time.Second)
	if rec.RetryCount < 1 {
		t.Fatalf("expected retry_count >= 1 due to injected failure, got %d", rec.RetryCount)
	}
	if pending := countPendingOutbox(t, db); pending != 0 {
		t.Fatalf("expected outbox pending = 0, got %d", pending)
	}
	if dlqLen := redisDLQLen(t, queue); dlqLen != 0 {
		t.Fatalf("expected dlq len = 0, got %d", dlqLen)
	}
	if attempts := handler.Attempts(taskID); attempts < 2 {
		t.Fatalf("expected attempts >= 2, got %d", attempts)
	}
}

func TestE2E_TaskPipeline_DLQReplay_ManualRecover(t *testing.T) {
	db := openTaskIntegrationDB(t)
	redisClient := openTaskIntegrationRedis(t)
	ctx := context.Background()

	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	outboxRepo := outbox.NewRepository(db)
	taskRepo := NewRepository(db, txm)
	queue, err := NewQueue(redisClient, QueueConfig{})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}

	handler := &toggleTaskHandler{}
	handler.SetFail(true)
	registry := NewHandlerRegistry()
	registry.Register(TaskTypeSendCouponNotice, handler)

	relay, err := outbox.NewRelay(outboxRepo, queue, outbox.RelayConfig{
		Enabled:      true,
		PollInterval: 20 * time.Millisecond,
		BatchSize:    100,
		BackoffBase:  20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new relay failed: %v", err)
	}
	consumer, err := NewConsumer(taskRepo, queue, registry, ConsumeConfig{
		Enabled:         true,
		Workers:         1,
		PollInterval:    20 * time.Millisecond,
		ReadyTimeout:    1 * time.Second,
		RetryBackoff:    20 * time.Millisecond,
		RetryMoveBatch:  100,
		DefaultMaxRetry: 2,
	})
	if err != nil {
		t.Fatalf("new consumer failed: %v", err)
	}

	startBackgroundComponents(t, ctx, relay, consumer)
	taskService := NewServiceWithQueue(txm, taskRepo, outboxRepo, queue, 2)
	server := newTaskHTTPServer(taskService)
	defer server.Close()

	taskID := createTaskViaHTTP(t, server.URL, "e2e_dlq_replay_"+nextTaskTestSuffix(), 2)
	waitTaskStatus(t, taskRepo, taskID, StatusDead, 8*time.Second)
	if dlqLen := redisDLQLen(t, queue); dlqLen == 0 {
		t.Fatalf("expected dlq len > 0 before replay")
	}

	handler.SetFail(false)
	replayTaskViaHTTP(t, server.URL, taskID)
	waitTaskStatus(t, taskRepo, taskID, StatusSuccess, 8*time.Second)
	if dlqLen := redisDLQLen(t, queue); dlqLen != 0 {
		t.Fatalf("expected dlq len = 0 after replay, got %d", dlqLen)
	}
}

func TestE2E_TaskPipeline_RetryScheduleFailure_RecoveredByCompensation(t *testing.T) {
	db := openTaskIntegrationDB(t)
	redisClient := openTaskIntegrationRedis(t)
	ctx := context.Background()

	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	outboxRepo := outbox.NewRepository(db)
	taskRepo := NewRepository(db, txm)
	realQueue, err := NewQueue(redisClient, QueueConfig{})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}
	queue := newScheduleFailureQueue(realQueue, 1)

	handler := &flakyTaskHandler{failTimes: 1}
	registry := NewHandlerRegistry()
	registry.Register(TaskTypeSendCouponNotice, handler)

	relay, err := outbox.NewRelay(outboxRepo, queue, outbox.RelayConfig{
		Enabled:      true,
		PollInterval: 20 * time.Millisecond,
		BatchSize:    100,
		BackoffBase:  20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new relay failed: %v", err)
	}
	consumer, err := NewConsumer(taskRepo, queue, registry, ConsumeConfig{
		Enabled:         true,
		Workers:         1,
		PollInterval:    20 * time.Millisecond,
		ReadyTimeout:    1 * time.Second,
		RetryBackoff:    20 * time.Millisecond,
		RetryMoveBatch:  100,
		DefaultMaxRetry: 3,
	})
	if err != nil {
		t.Fatalf("new consumer failed: %v", err)
	}
	compensator, err := NewCompensator(taskRepo, queue, CompensationConfig{
		Enabled:      true,
		PollInterval: 20 * time.Millisecond,
		BatchSize:    100,
	})
	if err != nil {
		t.Fatalf("new compensator failed: %v", err)
	}

	startBackgroundComponents(t, ctx, relay, consumer)
	if err := compensator.Start(ctx); err != nil {
		t.Fatalf("start compensator failed: %v", err)
	}
	t.Cleanup(func() {
		_ = compensator.Stop(context.Background())
	})

	taskService := NewServiceWithQueue(txm, taskRepo, outboxRepo, nil, 3)
	server := newTaskHTTPServer(taskService)
	defer server.Close()

	taskID := createTaskViaHTTP(t, server.URL, "e2e_compensate_"+nextTaskTestSuffix(), 3)
	rec := waitTaskStatus(t, taskRepo, taskID, StatusSuccess, 10*time.Second)
	if rec.RetryCount < 1 {
		t.Fatalf("expected retry_count >= 1, got %d", rec.RetryCount)
	}
	if queue.FailedScheduleCalls() < 1 {
		t.Fatalf("expected injected schedule failure to happen")
	}
	if pending := countPendingOutbox(t, db); pending != 0 {
		t.Fatalf("expected outbox pending = 0, got %d", pending)
	}
}
