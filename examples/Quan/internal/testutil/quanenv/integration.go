package quanenv

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mini-jupiter/pkg/mysql"
	"mini-jupiter/pkg/rabbitmq"
	"mini-jupiter/pkg/redis"

	mysqldriver "github.com/go-sql-driver/mysql"
)

const (
	TestMySQLDSNEnv        = "QUAN_TEST_MYSQL_DSN"
	DefaultTestMySQLDSN    = "root:root@tcp(127.0.0.1:3306)/mini_jupiter_coupon?parseTime=true&loc=Local&charset=utf8mb4"
	TestRedisModeEnv       = "QUAN_TEST_REDIS_MODE"
	TestRedisAddrEnv       = "QUAN_TEST_REDIS_ADDR"
	TestRedisAddrsEnv      = "QUAN_TEST_REDIS_ADDRS"
	TestRedisMasterNameEnv = "QUAN_TEST_REDIS_MASTER_NAME"
	DefaultTestRedisAddr   = "127.0.0.1:6379"
	DefaultTestRedisAddrs  = "127.0.0.1:26379,127.0.0.1:26380,127.0.0.1:26381"
	DefaultTestRedisMaster = "mymaster"
	TestRabbitMQURLEnv     = "QUAN_TEST_RABBITMQ_URL"
	DefaultTestRabbitMQURL = "amqp://guest:guest@127.0.0.1:5672/"
)

var couponIDSeed int64 = 900000

func OpenIntegrationDB(t *testing.T, suite string) *sql.DB {
	t.Helper()
	dsn, fromEnv := resolveTestMySQLDSN(t, suite)
	if err := ensureDatabaseExists(dsn); err != nil {
		if !fromEnv {
			t.Skipf("skip integration test: %s is not set and docker default mysql is unavailable: %v", TestMySQLDSNEnv, err)
		}
		t.Fatalf("ensure test database failed: %v", err)
	}

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
			t.Skipf("skip integration test: %s is not set and docker default mysql is unavailable: %v", TestMySQLDSNEnv, err)
		}
		t.Fatalf("ping mysql failed: %v", err)
	}

	resetSchema(t, db)
	sqlDir := resolveSQLDir(t)
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
	return db
}

func OpenIntegrationRedis(t *testing.T, dbIndex int) *redis.Client {
	t.Helper()
	cfg, fromEnv := resolveTestRedisConfig(dbIndex)
	client, err := redis.NewClient(cfg)
	if err != nil {
		t.Fatalf("new redis client failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(context.Background()); err != nil {
		if !fromEnv {
			t.Skipf("skip integration test: %s/%s are not set and docker default redis is unavailable: %v", TestRedisAddrEnv, TestRedisAddrsEnv, err)
		}
		t.Fatalf("ping redis failed: %v", err)
	}
	if err := client.Raw().FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("flush redis failed: %v", err)
	}
	return client
}

func OpenIntegrationRabbitMQ(t *testing.T) *rabbitmq.Client {
	t.Helper()
	url := strings.TrimSpace(os.Getenv(TestRabbitMQURLEnv))
	fromEnv := true
	if url == "" {
		url = DefaultTestRabbitMQURL
		fromEnv = false
	}
	client, err := rabbitmq.NewClient(rabbitmq.Config{
		URL:         url,
		DialTimeout: 5 * time.Second,
		Heartbeat:   10 * time.Second,
	})
	if err != nil {
		if !fromEnv {
			t.Skipf("skip integration test: %s is not set and docker default rabbitmq is unavailable: %v", TestRabbitMQURLEnv, err)
		}
		t.Fatalf("new rabbitmq client failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(context.Background()); err != nil {
		if !fromEnv {
			t.Skipf("skip integration test: %s is not set and docker default rabbitmq is unavailable: %v", TestRabbitMQURLEnv, err)
		}
		t.Fatalf("ping rabbitmq failed: %v", err)
	}
	return client
}

func resolveTestRedisConfig(dbIndex int) (redis.Config, bool) {
	mode := strings.TrimSpace(os.Getenv(TestRedisModeEnv))
	addr := strings.TrimSpace(os.Getenv(TestRedisAddrEnv))
	addrsEnv := strings.TrimSpace(os.Getenv(TestRedisAddrsEnv))
	masterName := strings.TrimSpace(os.Getenv(TestRedisMasterNameEnv))

	fromEnv := mode != "" || addr != "" || addrsEnv != "" || masterName != ""
	if mode == "" {
		if addrsEnv != "" || masterName != "" {
			mode = redis.ModeSentinel
		} else {
			mode = redis.ModeSimple
		}
	}

	cfg := redis.Config{
		Mode:        mode,
		Addr:        addr,
		MasterName:  masterName,
		DB:          dbIndex,
		DialTimeout: 2 * time.Second,
	}

	if addrsEnv != "" {
		cfg.Addrs = splitCSV(addrsEnv)
	}

	if !fromEnv {
		cfg.Mode = redis.ModeSimple
		cfg.Addr = DefaultTestRedisAddr
	}

	if cfg.Mode == redis.ModeSentinel && len(cfg.Addrs) == 0 {
		cfg.Addrs = splitCSV(DefaultTestRedisAddrs)
	}
	if cfg.Mode == redis.ModeSentinel && cfg.MasterName == "" {
		cfg.MasterName = DefaultTestRedisMaster
	}
	if cfg.Mode == redis.ModeSimple && cfg.Addr == "" {
		cfg.Addr = DefaultTestRedisAddr
	}

	return cfg, fromEnv
}

func NextCouponID() int64 {
	return atomic.AddInt64(&couponIDSeed, 1)
}

func CreateCampaign(t *testing.T, db *sql.DB, couponID int64, stock int, perUserLimit int) {
	t.Helper()
	_, err := db.Exec(`
INSERT INTO coupon_campaigns
	(coupon_id, name, total_stock, available_stock, per_user_limit, status, start_at, end_at, created_at, updated_at)
VALUES
	(?, ?, ?, ?, ?, 'ACTIVE', ?, ?, NOW(3), NOW(3))
`, couponID, fmt.Sprintf("it_coupon_%d", couponID), stock, stock, perUserLimit, time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("insert campaign failed: %v", err)
	}
}

func ResetTestData(t *testing.T, db *sql.DB, couponID int64) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM task_consume_receipts`); err != nil {
		t.Fatalf("cleanup task_consume_receipts failed: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM claim_side_effects`); err != nil {
		t.Fatalf("cleanup claim_side_effects failed: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM outbox_events`); err != nil {
		t.Fatalf("cleanup outbox_events failed: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM async_tasks`); err != nil {
		t.Fatalf("cleanup async_tasks failed: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM coupon_claims WHERE coupon_id = ?`, couponID); err != nil {
		t.Fatalf("cleanup coupon_claims failed: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM coupon_campaigns WHERE coupon_id = ?`, couponID); err != nil {
		t.Fatalf("cleanup coupon_campaigns failed: %v", err)
	}
}

func CountAsyncTasksTotal(t *testing.T, db *sql.DB) int {
	t.Helper()
	var cnt int
	if err := db.QueryRow(`SELECT COUNT(1) FROM async_tasks`).Scan(&cnt); err != nil {
		t.Fatalf("count async tasks failed: %v", err)
	}
	return cnt
}

func CountOutboxEventsTotal(t *testing.T, db *sql.DB) int {
	t.Helper()
	var cnt int
	if err := db.QueryRow(`SELECT COUNT(1) FROM outbox_events`).Scan(&cnt); err != nil {
		t.Fatalf("count outbox events failed: %v", err)
	}
	return cnt
}

func LoadRedisCampaignStock(t *testing.T, client *redis.Client, couponID int64, key func(int64) string) int {
	t.Helper()
	val, err := client.Raw().Get(context.Background(), key(couponID)).Int()
	if err != nil {
		t.Fatalf("load redis campaign stock failed: %v", err)
	}
	return val
}

func LoadRedisUserCount(t *testing.T, client *redis.Client, couponID, userID int64, key func(int64) string) int {
	t.Helper()
	val, err := client.Raw().HGet(context.Background(), key(couponID), fmt.Sprintf("%d", userID)).Int()
	if err != nil {
		if strings.Contains(err.Error(), "redis: nil") {
			return 0
		}
		t.Fatalf("load redis user count failed: %v", err)
	}
	return val
}

func LoadRedisString(t *testing.T, client *redis.Client, key string) string {
	t.Helper()
	val, err := client.Raw().Get(context.Background(), key).Result()
	if err != nil {
		if strings.Contains(err.Error(), "redis: nil") {
			return ""
		}
		t.Fatalf("load redis string failed: %v", err)
	}
	return val
}

func LoadRedisHashField(t *testing.T, client *redis.Client, key, field string) string {
	t.Helper()
	val, err := client.Raw().HGet(context.Background(), key, field).Result()
	if err != nil {
		if strings.Contains(err.Error(), "redis: nil") {
			return ""
		}
		t.Fatalf("load redis hash field failed: %v", err)
	}
	return val
}

func resolveTestMySQLDSN(t *testing.T, suite string) (string, bool) {
	dsn := strings.TrimSpace(os.Getenv(TestMySQLDSNEnv))
	if dsn != "" {
		return dsn, true
	}
	cfg, err := mysqldriver.ParseDSN(DefaultTestMySQLDSN)
	if err != nil {
		t.Fatalf("parse default mysql dsn failed: %v", err)
	}
	if suite == "" {
		suite = "quan"
	}
	cfg.DBName = fmt.Sprintf("mini_jupiter_%s_%d", suite, atomic.AddInt64(&couponIDSeed, 1))
	return cfg.FormatDSN(), false
}

func ensureDatabaseExists(dsn string) error {
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return fmt.Errorf("parse mysql dsn failed: %w", err)
	}
	if cfg.DBName == "" {
		return nil
	}
	targetDB := cfg.DBName

	adminCfg := *cfg
	adminCfg.DBName = ""
	adminDSN := adminCfg.FormatDSN()
	adminDB, err := sql.Open("mysql", adminDSN)
	if err != nil {
		return fmt.Errorf("open mysql admin connection failed: %w", err)
	}
	defer adminDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := adminDB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping mysql admin connection failed: %w", err)
	}
	createDBSQL := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", escapeBackticks(targetDB))
	if _, err := adminDB.ExecContext(ctx, createDBSQL); err != nil {
		return fmt.Errorf("create test database failed: %w", err)
	}
	return nil
}

func resolveSQLDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve sql dir caller failed")
	}
	sqlDir := filepath.Join(filepath.Dir(file), "..", "..", "..", "sql")
	abs, err := filepath.Abs(sqlDir)
	if err != nil {
		t.Fatalf("resolve sql dir failed: %v", err)
	}
	return abs
}

func escapeBackticks(s string) string {
	return strings.ReplaceAll(s, "`", "``")
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func resetSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`DROP TABLE IF EXISTS task_consume_receipts`,
		`DROP TABLE IF EXISTS claim_side_effects`,
		`DROP TABLE IF EXISTS async_tasks`,
		`DROP TABLE IF EXISTS outbox_events`,
		`DROP TABLE IF EXISTS coupon_claims`,
		`DROP TABLE IF EXISTS coupon_campaigns`,
		`DROP TABLE IF EXISTS schema_migrations`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("reset coupon schema failed for %q: %v", stmt, err)
		}
	}
}
