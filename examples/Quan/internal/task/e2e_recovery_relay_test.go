package task

import (
	"context"
	"errors"
	"testing"
	"time"

	"mini-jupiter/examples/Quan/internal/outbox"
	"mini-jupiter/pkg/mysql"
)

func TestE2E_FaultInjection_RelayPublishFailure_RetryThenRecover(t *testing.T) {
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
	queue := newInjectedQueue(realQueue, injectedQueueConfig{
		publishReadyFailures: 1,
		publishReadyErr:      errors.New("injected relay publish failure"),
	})

	handler := &flakyTaskHandler{}
	registry := NewHandlerRegistry()
	registry.Register(TaskTypeSendCouponNotice, handler)

	relay, err := outbox.NewRelay(outboxRepo, queue, outbox.RelayConfig{
		Enabled:         true,
		PollInterval:    20 * time.Millisecond,
		BatchSize:       100,
		BackoffBase:     20 * time.Millisecond,
		DispatchTimeout: 50 * time.Millisecond,
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
	svc := NewServiceWithQueue(txm, taskRepo, outboxRepo, queue, 3)
	taskID := createTaskViaService(t, svc, "e2e_relay_publish_fail_"+nextTaskTestSuffix(), 3)

	waitTaskStatus(t, taskRepo, taskID, StatusSuccess, 10*time.Second)
	waitOutboxEventStatus(t, db, taskID, outbox.StatusPublished, 10*time.Second)
	if queue.publishReadyFault.FiredCount() < 1 {
		t.Fatalf("expected injected publish failure to fire")
	}
}

func TestE2E_FaultInjection_RelayMarkPublishedFailure_RecoveredByDispatchScan(t *testing.T) {
	db := openTaskIntegrationDB(t)
	redisClient := openTaskIntegrationRedis(t)
	ctx := context.Background()

	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	realOutboxRepo := outbox.NewRepository(db)
	relayRepo := newInjectedRelayRepo(realOutboxRepo, 1, errors.New("injected mark published failure"))
	taskRepo := NewRepository(db, txm)
	queue, err := NewQueue(redisClient, QueueConfig{})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}

	handler := &flakyTaskHandler{}
	registry := NewHandlerRegistry()
	registry.Register(TaskTypeSendCouponNotice, handler)

	relay, err := outbox.NewRelay(relayRepo, queue, outbox.RelayConfig{
		Enabled:         true,
		PollInterval:    20 * time.Millisecond,
		BatchSize:       100,
		BackoffBase:     20 * time.Millisecond,
		DispatchTimeout: 50 * time.Millisecond,
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
	svc := NewServiceWithQueue(txm, taskRepo, realOutboxRepo, queue, 3)
	taskID := createTaskViaService(t, svc, "e2e_mark_published_fail_"+nextTaskTestSuffix(), 3)

	waitTaskStatus(t, taskRepo, taskID, StatusSuccess, 10*time.Second)
	waitOutboxEventStatus(t, db, taskID, outbox.StatusPublished, 10*time.Second)
	if relayRepo.markPublishedFault.FiredCount() < 1 {
		t.Fatalf("expected injected mark-published failure to fire")
	}
}

func TestE2E_FaultInjection_ShortRedisOutageOnPublishReady_Recovered(t *testing.T) {
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
	queue := newInjectedQueue(realQueue, injectedQueueConfig{
		publishReadyFailures: 3,
		publishReadyErr:      errors.New("injected short redis outage"),
	})

	handler := &flakyTaskHandler{}
	registry := NewHandlerRegistry()
	registry.Register(TaskTypeSendCouponNotice, handler)

	relay, err := outbox.NewRelay(outboxRepo, queue, outbox.RelayConfig{
		Enabled:         true,
		PollInterval:    20 * time.Millisecond,
		BatchSize:       100,
		BackoffBase:     20 * time.Millisecond,
		DispatchTimeout: 50 * time.Millisecond,
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
	svc := NewServiceWithQueue(txm, taskRepo, outboxRepo, queue, 3)
	taskID := createTaskViaService(t, svc, "e2e_short_outage_"+nextTaskTestSuffix(), 3)

	waitTaskStatus(t, taskRepo, taskID, StatusSuccess, 12*time.Second)
	waitOutboxEventStatus(t, db, taskID, outbox.StatusPublished, 12*time.Second)
	if queue.publishReadyFault.FiredCount() < 3 {
		t.Fatalf("expected simulated short redis outage to fire at least 3 times")
	}
}
