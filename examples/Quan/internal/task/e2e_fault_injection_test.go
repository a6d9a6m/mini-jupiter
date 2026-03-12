package task

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mini-jupiter/examples/Quan/internal/outbox"
	"mini-jupiter/pkg/mysql"
	"mini-jupiter/pkg/redis"

	mysqldriver "github.com/go-sql-driver/mysql"
)

const (
	testTaskMySQLDSNEnv  = "QUAN_TEST_MYSQL_DSN"
	testTaskRedisAddrEnv = "QUAN_TEST_REDIS_ADDR"
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

	taskID := createTaskViaHTTP(t, server.URL, "e2e_retry_success_"+strconv.FormatInt(time.Now().UnixNano(), 10), 3)
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

	taskID := createTaskViaHTTP(t, server.URL, "e2e_dlq_replay_"+strconv.FormatInt(time.Now().UnixNano(), 10), 2)
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

	taskID := createTaskViaHTTP(t, server.URL, "e2e_compensate_"+strconv.FormatInt(time.Now().UnixNano(), 10), 3)
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

func startBackgroundComponents(t *testing.T, ctx context.Context, relay *outbox.Relay, consumer *Consumer) {
	t.Helper()
	if err := relay.Start(ctx); err != nil {
		t.Fatalf("start relay failed: %v", err)
	}
	if err := consumer.Start(ctx); err != nil {
		t.Fatalf("start consumer failed: %v", err)
	}
	t.Cleanup(func() {
		_ = consumer.Stop(context.Background())
		_ = relay.Stop(context.Background())
	})
}

func newTaskHTTPServer(svc *Service) *httptest.Server {
	mux := http.NewServeMux()
	NewHTTPHandler(svc).Register(mux)
	return httptest.NewServer(mux)
}

func createTaskViaHTTP(t *testing.T, baseURL, bizID string, maxRetry int) int64 {
	t.Helper()
	reqBody := map[string]any{
		"task_type": TaskTypeSendCouponNotice,
		"biz_id":    bizID,
		"payload": map[string]any{
			"claim_id":  1001,
			"coupon_id": 2001,
			"user_id":   3001,
		},
		"max_retry": maxRetry,
	}
	body, _ := json.Marshal(reqBody)
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/tasks", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new create task request failed: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", "1")
	req.Header.Set("Idempotency-Key", "idem-"+bizID)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create task request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create task status code = %d", resp.StatusCode)
	}
	var payload struct {
		Data struct {
			TaskID int64 `json:"task_id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode create task response failed: %v", err)
	}
	if payload.Data.TaskID <= 0 {
		t.Fatalf("invalid task id from response: %d", payload.Data.TaskID)
	}
	return payload.Data.TaskID
}

func replayTaskViaHTTP(t *testing.T, baseURL string, taskID int64) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/v1/tasks/%d/replay", baseURL, taskID), nil)
	if err != nil {
		t.Fatalf("new replay task request failed: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("replay task request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replay task status code = %d", resp.StatusCode)
	}
}

func waitTaskStatus(t *testing.T, repo *Repository, taskID int64, want string, timeout time.Duration) AsyncTask {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		rec, err := repo.GetByID(context.Background(), taskID)
		if err == nil && rec.Status == want {
			return rec
		}
		time.Sleep(50 * time.Millisecond)
	}
	rec, err := repo.GetByID(context.Background(), taskID)
	if err != nil {
		t.Fatalf("query task status timeout with err: %v", err)
	}
	t.Fatalf("wait task status timeout: want=%s got=%s", want, rec.Status)
	return AsyncTask{}
}

func countPendingOutbox(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var cnt int64
	if err := db.QueryRow(`SELECT COUNT(1) FROM outbox_events WHERE status='PENDING'`).Scan(&cnt); err != nil {
		t.Fatalf("count pending outbox failed: %v", err)
	}
	return cnt
}

func redisDLQLen(t *testing.T, q *Queue) int64 {
	t.Helper()
	n, err := q.rdb.LLen(context.Background(), q.cfg.DLQKey).Result()
	if err != nil {
		t.Fatalf("query dlq len failed: %v", err)
	}
	return n
}

func openTaskIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(testTaskMySQLDSNEnv))
	if dsn == "" {
		t.Skipf("skip e2e test: %s is not set", testTaskMySQLDSNEnv)
	}
	ensureTaskDatabaseExists(t, dsn)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open mysql failed: %v", err)
	}
	db.SetMaxOpenConns(40)
	db.SetMaxIdleConns(20)
	db.SetConnMaxLifetime(30 * time.Minute)
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping mysql failed: %v", err)
	}

	sqlDir, err := filepath.Abs(filepath.Join("..", "..", "sql"))
	if err != nil {
		t.Fatalf("resolve sql dir failed: %v", err)
	}
	migrator, err := mysql.NewMigrator(db, mysql.MigrationConfig{
		Dir:       sqlDir,
		TableName: "schema_migrations",
	})
	if err != nil {
		t.Fatalf("new migrator failed: %v", err)
	}
	if err := migrator.Run(ctx); err != nil {
		t.Fatalf("run migrations failed: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM async_tasks`); err != nil {
		t.Fatalf("cleanup async_tasks failed: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM outbox_events`); err != nil {
		t.Fatalf("cleanup outbox_events failed: %v", err)
	}
	return db
}

func openTaskIntegrationRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := strings.TrimSpace(os.Getenv(testTaskRedisAddrEnv))
	if addr == "" {
		t.Skipf("skip e2e test: %s is not set", testTaskRedisAddrEnv)
	}
	client, err := redis.NewClient(redis.Config{
		Addr:        addr,
		DB:          0,
		DialTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("new redis client failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("ping redis failed: %v", err)
	}
	if err := client.Raw().FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("flush redis failed: %v", err)
	}
	return client
}

func ensureTaskDatabaseExists(t *testing.T, dsn string) {
	t.Helper()
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse mysql dsn failed: %v", err)
	}
	if cfg.DBName == "" {
		return
	}
	targetDB := cfg.DBName
	adminCfg := *cfg
	adminCfg.DBName = ""
	adminDSN := adminCfg.FormatDSN()
	adminDB, err := sql.Open("mysql", adminDSN)
	if err != nil {
		t.Fatalf("open mysql admin connection failed: %v", err)
	}
	defer adminDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := adminDB.PingContext(ctx); err != nil {
		t.Fatalf("ping mysql admin connection failed: %v", err)
	}
	createDBSQL := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", escapeTaskBackticks(targetDB))
	if _, err := adminDB.ExecContext(ctx, createDBSQL); err != nil {
		t.Fatalf("create test database failed: %v", err)
	}
}

func escapeTaskBackticks(s string) string {
	return strings.ReplaceAll(s, "`", "``")
}

type flakyTaskHandler struct {
	failTimes int
	mu        sync.Mutex
	attempts  map[int64]int
}

func (h *flakyTaskHandler) Handle(_ context.Context, task AsyncTask) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.attempts == nil {
		h.attempts = map[int64]int{}
	}
	h.attempts[task.ID]++
	if h.attempts[task.ID] <= h.failTimes {
		return errors.New("injected handler failure")
	}
	return nil
}

func (h *flakyTaskHandler) Attempts(taskID int64) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.attempts[taskID]
}

type toggleTaskHandler struct {
	fail atomic.Bool
}

func (h *toggleTaskHandler) Handle(_ context.Context, _ AsyncTask) error {
	if h.fail.Load() {
		return errors.New("injected always fail")
	}
	return nil
}

func (h *toggleTaskHandler) SetFail(v bool) {
	h.fail.Store(v)
}

type scheduleFailureQueue struct {
	inner           *Queue
	remainFailCalls atomic.Int32
	failedCalls     atomic.Int32
}

func newScheduleFailureQueue(inner *Queue, failCalls int32) *scheduleFailureQueue {
	q := &scheduleFailureQueue{inner: inner}
	q.remainFailCalls.Store(failCalls)
	return q
}

func (q *scheduleFailureQueue) PublishReady(ctx context.Context, taskID int64) error {
	return q.inner.PublishReady(ctx, taskID)
}

func (q *scheduleFailureQueue) PushDLQ(ctx context.Context, taskID int64) error {
	return q.inner.PushDLQ(ctx, taskID)
}

func (q *scheduleFailureQueue) ReplayFromDLQ(ctx context.Context, taskID int64) (bool, error) {
	return q.inner.ReplayFromDLQ(ctx, taskID)
}

func (q *scheduleFailureQueue) MoveDueRetryToReady(ctx context.Context, batch int) (int, error) {
	return q.inner.MoveDueRetryToReady(ctx, batch)
}

func (q *scheduleFailureQueue) PopReady(ctx context.Context, timeout time.Duration) (int64, bool, error) {
	return q.inner.PopReady(ctx, timeout)
}

func (q *scheduleFailureQueue) ScheduleRetry(ctx context.Context, taskID int64, retryAt time.Time) error {
	for {
		remain := q.remainFailCalls.Load()
		if remain <= 0 {
			break
		}
		if q.remainFailCalls.CompareAndSwap(remain, remain-1) {
			q.failedCalls.Add(1)
			return errors.New("injected schedule retry failure")
		}
	}
	return q.inner.ScheduleRetry(ctx, taskID, retryAt)
}

func (q *scheduleFailureQueue) FailedScheduleCalls() int32 {
	return q.failedCalls.Load()
}
