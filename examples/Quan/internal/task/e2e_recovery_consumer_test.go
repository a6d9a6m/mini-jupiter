package task

import (
	"context"
	"errors"
	"testing"
	"time"

	"mini-jupiter/pkg/mysql"
)

func TestE2E_FaultInjection_ConsumerMarkSuccessFailure_RecoveredByCompensation(t *testing.T) {
	db := openTaskIntegrationDB(t)
	redisClient := openTaskIntegrationRedis(t)
	ctx := context.Background()

	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	taskRepo := NewRepository(db, txm)
	injectedRepo := newInjectedTaskRepo(taskRepo, 1, errors.New("injected mark success failure"))
	queue, err := NewQueue(redisClient, QueueConfig{})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}

	handler := &flakyTaskHandler{}
	registry := NewHandlerRegistry()
	registry.Register(TaskTypeSendCouponNotice, handler)

	consumer, err := NewConsumer(injectedRepo, queue, registry, ConsumeConfig{
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
	compensator, err := NewCompensator(injectedRepo, queue, CompensationConfig{
		Enabled:      true,
		PollInterval: 20 * time.Millisecond,
		BatchSize:    100,
		StaleTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new compensator failed: %v", err)
	}

	if err := consumer.Start(ctx); err != nil {
		t.Fatalf("start consumer failed: %v", err)
	}
	if err := compensator.Start(ctx); err != nil {
		t.Fatalf("start compensator failed: %v", err)
	}
	t.Cleanup(func() {
		_ = compensator.Stop(context.Background())
		_ = consumer.Stop(context.Background())
	})

	taskID := createTaskDirect(t, taskRepo, "e2e_mark_success_fail_"+nextTaskTestSuffix(), 3)
	if err := queue.PublishReady(ctx, taskID); err != nil {
		t.Fatalf("publish ready failed: %v", err)
	}

	waitTaskStatus(t, taskRepo, taskID, StatusSuccess, 10*time.Second)
	if injectedRepo.markSuccessFault.FiredCount() < 1 {
		t.Fatalf("expected injected mark-success failure to fire")
	}
	if attempts := handler.Attempts(taskID); attempts < 2 {
		t.Fatalf("expected duplicate consume after suspended recovery, got attempts=%d", attempts)
	}
}

func TestE2E_FaultInjection_DuplicateReadyDelivery_ConsumesOnce(t *testing.T) {
	db := openTaskIntegrationDB(t)
	redisClient := openTaskIntegrationRedis(t)
	ctx := context.Background()

	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	taskRepo := NewRepository(db, txm)
	queue, err := NewQueue(redisClient, QueueConfig{})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}

	handler := &flakyTaskHandler{}
	registry := NewHandlerRegistry()
	registry.Register(TaskTypeSendCouponNotice, handler)
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

	if err := consumer.Start(ctx); err != nil {
		t.Fatalf("start consumer failed: %v", err)
	}
	t.Cleanup(func() {
		_ = consumer.Stop(context.Background())
	})

	taskID := createTaskDirect(t, taskRepo, "e2e_duplicate_delivery_"+nextTaskTestSuffix(), 3)
	if err := queue.PublishReady(ctx, taskID); err != nil {
		t.Fatalf("publish ready failed: %v", err)
	}
	if err := queue.PublishReady(ctx, taskID); err != nil {
		t.Fatalf("publish duplicate ready failed: %v", err)
	}

	waitTaskStatus(t, taskRepo, taskID, StatusSuccess, 8*time.Second)
	time.Sleep(200 * time.Millisecond)
	if attempts := handler.Attempts(taskID); attempts != 1 {
		t.Fatalf("expected handler to run exactly once under duplicate delivery, got %d", attempts)
	}
}

func TestE2E_FaultInjection_StaleRunningRecoveredAfterRestartLikePause(t *testing.T) {
	db := openTaskIntegrationDB(t)
	redisClient := openTaskIntegrationRedis(t)
	ctx := context.Background()

	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	taskRepo := NewRepository(db, txm)
	queue, err := NewQueue(redisClient, QueueConfig{})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}

	handler := &flakyTaskHandler{}
	registry := NewHandlerRegistry()
	registry.Register(TaskTypeSendCouponNotice, handler)
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
		StaleTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new compensator failed: %v", err)
	}

	taskID := createTaskDirect(t, taskRepo, "e2e_stale_running_"+nextTaskTestSuffix(), 3)
	if _, ok, err := taskRepo.TryMarkRunning(ctx, taskID); err != nil || !ok {
		t.Fatalf("pre-mark task running failed: ok=%v err=%v", ok, err)
	}

	if err := consumer.Start(ctx); err != nil {
		t.Fatalf("start consumer failed: %v", err)
	}
	if err := compensator.Start(ctx); err != nil {
		t.Fatalf("start compensator failed: %v", err)
	}
	t.Cleanup(func() {
		_ = compensator.Stop(context.Background())
		_ = consumer.Stop(context.Background())
	})

	waitTaskStatus(t, taskRepo, taskID, StatusSuccess, 10*time.Second)
	if attempts := handler.Attempts(taskID); attempts != 1 {
		t.Fatalf("expected one handler execution after stale running recovery, got %d", attempts)
	}
}

func TestE2E_FaultInjection_ConsumerMarkSuccessFailure_DeduplicatesSideEffectByUniqueKey(t *testing.T) {
	db := openTaskIntegrationDB(t)
	redisClient := openTaskIntegrationRedis(t)
	ctx := context.Background()

	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	taskRepo := NewRepository(db, txm)
	injectedRepo := newInjectedTaskRepo(taskRepo, 1, errors.New("injected mark success failure"))
	queue, err := NewQueue(redisClient, QueueConfig{})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}

	registry := NewHandlerRegistry()
	registry.Register(TaskTypeSendCouponNotice, NewSendCouponNoticeHandler(NewConsumeReceiptRepository(db)))

	consumer, err := NewConsumer(injectedRepo, queue, registry, ConsumeConfig{
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
	compensator, err := NewCompensator(injectedRepo, queue, CompensationConfig{
		Enabled:      true,
		PollInterval: 20 * time.Millisecond,
		BatchSize:    100,
		StaleTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new compensator failed: %v", err)
	}

	if err := consumer.Start(ctx); err != nil {
		t.Fatalf("start consumer failed: %v", err)
	}
	if err := compensator.Start(ctx); err != nil {
		t.Fatalf("start compensator failed: %v", err)
	}
	t.Cleanup(func() {
		_ = compensator.Stop(context.Background())
		_ = consumer.Stop(context.Background())
	})

	bizID := "e2e_mark_success_dedupe_" + nextTaskTestSuffix()
	taskID := createTaskDirect(t, taskRepo, bizID, 3)
	if err := queue.PublishReady(ctx, taskID); err != nil {
		t.Fatalf("publish ready failed: %v", err)
	}

	waitTaskStatus(t, taskRepo, taskID, StatusSuccess, 10*time.Second)
	if got := countConsumeReceipts(t, db, TaskTypeSendCouponNotice, bizID); got != 1 {
		t.Fatalf("expected one consume receipt after recovery replay, got %d", got)
	}
}
