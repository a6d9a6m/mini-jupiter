package task

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"sync/atomic"
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
	taskID := createTaskViaService(t, svc, "e2e_relay_publish_fail_"+strconv.FormatInt(time.Now().UnixNano(), 10), 3)

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
	taskID := createTaskViaService(t, svc, "e2e_mark_published_fail_"+strconv.FormatInt(time.Now().UnixNano(), 10), 3)

	waitTaskStatus(t, taskRepo, taskID, StatusSuccess, 10*time.Second)
	waitOutboxEventStatus(t, db, taskID, outbox.StatusPublished, 10*time.Second)
	if relayRepo.markPublishedFault.FiredCount() < 1 {
		t.Fatalf("expected injected mark-published failure to fire")
	}
}

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

	taskID := createTaskDirect(t, taskRepo, "e2e_mark_success_fail_"+strconv.FormatInt(time.Now().UnixNano(), 10), 3)
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

	taskID := createTaskDirect(t, taskRepo, "e2e_duplicate_delivery_"+strconv.FormatInt(time.Now().UnixNano(), 10), 3)
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
	taskID := createTaskViaService(t, svc, "e2e_short_outage_"+strconv.FormatInt(time.Now().UnixNano(), 10), 3)

	waitTaskStatus(t, taskRepo, taskID, StatusSuccess, 12*time.Second)
	waitOutboxEventStatus(t, db, taskID, outbox.StatusPublished, 12*time.Second)
	if queue.publishReadyFault.FiredCount() < 3 {
		t.Fatalf("expected simulated short redis outage to fire at least 3 times")
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

	taskID := createTaskDirect(t, taskRepo, "e2e_stale_running_"+strconv.FormatInt(time.Now().UnixNano(), 10), 3)
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

type injectedQueueConfig struct {
	publishReadyFailures  int32
	publishReadyErr       error
	scheduleRetryFailures int32
	scheduleRetryErr      error
}

type injectedQueue struct {
	inner              *Queue
	publishReadyFault  *deterministicFault
	scheduleRetryFault *deterministicFault
}

func newInjectedQueue(inner *Queue, cfg injectedQueueConfig) *injectedQueue {
	return &injectedQueue{
		inner:              inner,
		publishReadyFault:  newDeterministicFault(cfg.publishReadyFailures, cfg.publishReadyErr),
		scheduleRetryFault: newDeterministicFault(cfg.scheduleRetryFailures, cfg.scheduleRetryErr),
	}
}

func (q *injectedQueue) PublishReady(ctx context.Context, taskID int64) error {
	if err := q.publishReadyFault.Fail(); err != nil {
		return err
	}
	return q.inner.PublishReady(ctx, taskID)
}

func (q *injectedQueue) PushDLQ(ctx context.Context, taskID int64) error {
	return q.inner.PushDLQ(ctx, taskID)
}

func (q *injectedQueue) ReplayFromDLQ(ctx context.Context, taskID int64) (bool, error) {
	return q.inner.ReplayFromDLQ(ctx, taskID)
}

func (q *injectedQueue) MoveDueRetryToReady(ctx context.Context, batch int) (int, error) {
	return q.inner.MoveDueRetryToReady(ctx, batch)
}

func (q *injectedQueue) PopReady(ctx context.Context, timeout time.Duration) (int64, bool, error) {
	return q.inner.PopReady(ctx, timeout)
}

func (q *injectedQueue) ScheduleRetry(ctx context.Context, taskID int64, retryAt time.Time) error {
	if err := q.scheduleRetryFault.Fail(); err != nil {
		return err
	}
	return q.inner.ScheduleRetry(ctx, taskID, retryAt)
}

type injectedRelayRepo struct {
	inner              *outbox.Repository
	markPublishedFault *deterministicFault
}

func newInjectedRelayRepo(inner *outbox.Repository, failures int32, err error) *injectedRelayRepo {
	return &injectedRelayRepo{
		inner:              inner,
		markPublishedFault: newDeterministicFault(failures, err),
	}
}

func (r *injectedRelayRepo) ListDispatchable(ctx context.Context, limit int) ([]outbox.Event, error) {
	return r.inner.ListDispatchable(ctx, limit)
}

func (r *injectedRelayRepo) CountPending(ctx context.Context) (int64, error) {
	return r.inner.CountPending(ctx)
}

func (r *injectedRelayRepo) TryMarkDispatching(ctx context.Context, eventID int64) (bool, error) {
	return r.inner.TryMarkDispatching(ctx, eventID)
}

func (r *injectedRelayRepo) MarkPublished(ctx context.Context, eventID int64) error {
	if err := r.markPublishedFault.Fail(); err != nil {
		return err
	}
	return r.inner.MarkPublished(ctx, eventID)
}

func (r *injectedRelayRepo) MarkRetry(ctx context.Context, eventID int64, delay time.Duration, lastErr string) error {
	return r.inner.MarkRetry(ctx, eventID, delay, lastErr)
}

func (r *injectedRelayRepo) MarkSuspended(ctx context.Context, eventID int64, lastErr string) error {
	return r.inner.MarkSuspended(ctx, eventID, lastErr)
}

func (r *injectedRelayRepo) RecoverStaleDispatching(ctx context.Context, staleBefore time.Time, limit int) (int64, error) {
	return r.inner.RecoverStaleDispatching(ctx, staleBefore, limit)
}

type injectedTaskRepo struct {
	inner            *Repository
	markSuccessFault *deterministicFault
}

func newInjectedTaskRepo(inner *Repository, failures int32, err error) *injectedTaskRepo {
	return &injectedTaskRepo{
		inner:            inner,
		markSuccessFault: newDeterministicFault(failures, err),
	}
}

func (r *injectedTaskRepo) TryMarkRunning(ctx context.Context, taskID int64) (AsyncTask, bool, error) {
	return r.inner.TryMarkRunning(ctx, taskID)
}

func (r *injectedTaskRepo) MarkFailed(ctx context.Context, taskID int64, lastErr string, backoffBase time.Duration) (bool, *time.Time, error) {
	return r.inner.MarkFailed(ctx, taskID, lastErr, backoffBase)
}

func (r *injectedTaskRepo) MarkSuccess(ctx context.Context, taskID int64) error {
	if err := r.markSuccessFault.Fail(); err != nil {
		return err
	}
	return r.inner.MarkSuccess(ctx, taskID)
}

func (r *injectedTaskRepo) MarkSuspended(ctx context.Context, taskID int64, lastErr string) error {
	return r.inner.MarkSuspended(ctx, taskID, lastErr)
}

func (r *injectedTaskRepo) ListDueFailedForCompensation(ctx context.Context, limit int) ([]int64, error) {
	return r.inner.ListDueFailedForCompensation(ctx, limit)
}

func (r *injectedTaskRepo) ListSuspendedForCompensation(ctx context.Context, staleBefore time.Time, limit int) ([]int64, error) {
	return r.inner.ListSuspendedForCompensation(ctx, staleBefore, limit)
}

func (r *injectedTaskRepo) ListStaleRunningForCompensation(ctx context.Context, staleBefore time.Time, limit int) ([]int64, error) {
	return r.inner.ListStaleRunningForCompensation(ctx, staleBefore, limit)
}

func (r *injectedTaskRepo) MarkRecoveredForRetry(ctx context.Context, taskID int64, lastErr string) (bool, error) {
	return r.inner.MarkRecoveredForRetry(ctx, taskID, lastErr)
}

type deterministicFault struct {
	remaining atomic.Int32
	fired     atomic.Int32
	err       error
}

func newDeterministicFault(times int32, err error) *deterministicFault {
	f := &deterministicFault{err: err}
	f.remaining.Store(times)
	return f
}

func (f *deterministicFault) Fail() error {
	if f == nil {
		return nil
	}
	for {
		remain := f.remaining.Load()
		if remain <= 0 {
			return nil
		}
		if f.remaining.CompareAndSwap(remain, remain-1) {
			f.fired.Add(1)
			if f.err != nil {
				return f.err
			}
			return errors.New("injected deterministic fault")
		}
	}
}

func (f *deterministicFault) FiredCount() int32 {
	if f == nil {
		return 0
	}
	return f.fired.Load()
}

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
