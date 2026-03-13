package task

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	defaultTaskMySQLDSN  = "root:root@tcp(127.0.0.1:3306)/mini_jupiter?parseTime=true&loc=Local&charset=utf8mb4"
	defaultTaskRedisAddr = "127.0.0.1:6379"
)

var taskTestSeed atomic.Int64

func nextTaskTestSuffix() string {
	return strconv.FormatInt(time.Now().UnixNano()+taskTestSeed.Add(1), 10)
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
	dsn, fromEnv := resolveTaskMySQLDSN()
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
		if !fromEnv {
			t.Skipf("skip e2e test: %s is not set and docker default mysql is unavailable: %v", testTaskMySQLDSNEnv, err)
		}
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
	addr, fromEnv := resolveTaskRedisAddr()
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
		if !fromEnv {
			t.Skipf("skip e2e test: %s is not set and docker default redis is unavailable: %v", testTaskRedisAddrEnv, err)
		}
		t.Fatalf("ping redis failed: %v", err)
	}
	if err := client.Raw().FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("flush redis failed: %v", err)
	}
	return client
}

func resolveTaskMySQLDSN() (string, bool) {
	dsn := strings.TrimSpace(os.Getenv(testTaskMySQLDSNEnv))
	if dsn != "" {
		return dsn, true
	}
	return defaultTaskMySQLDSN, false
}

func resolveTaskRedisAddr() (string, bool) {
	addr := strings.TrimSpace(os.Getenv(testTaskRedisAddrEnv))
	if addr != "" {
		return addr, true
	}
	return defaultTaskRedisAddr, false
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
