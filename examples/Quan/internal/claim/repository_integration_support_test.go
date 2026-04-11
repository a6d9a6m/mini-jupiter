package claim

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mini-jupiter/pkg/mysql"
	"mini-jupiter/pkg/redis"

	mysqldriver "github.com/go-sql-driver/mysql"
)

const testMySQLDSNEnv = "QUAN_TEST_MYSQL_DSN"
const defaultTestMySQLDSN = "root:root@tcp(127.0.0.1:3306)/mini_jupiter_coupon?parseTime=true&loc=Local&charset=utf8mb4"

var testCouponIDSeed int64 = 900000

func openIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn, fromEnv := resolveTestMySQLDSN(t)
	if err := ensureDatabaseExists(dsn); err != nil {
		if !fromEnv {
			t.Skipf("skip integration test: %s is not set and docker default mysql is unavailable: %v", testMySQLDSNEnv, err)
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
			t.Skipf("skip integration test: %s is not set and docker default mysql is unavailable: %v", testMySQLDSNEnv, err)
		}
		t.Fatalf("ping mysql failed: %v", err)
	}

	sqlDir, err := filepath.Abs(filepath.Join("..", "..", "sql"))
	if err != nil {
		t.Fatalf("resolve sql dir failed: %v", err)
	}
	resetCouponSchema(t, db)
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

func resolveTestMySQLDSN(t *testing.T) (string, bool) {
	dsn := strings.TrimSpace(os.Getenv(testMySQLDSNEnv))
	if dsn != "" {
		return dsn, true
	}
	cfg, err := mysqldriver.ParseDSN(defaultTestMySQLDSN)
	if err != nil {
		t.Fatalf("parse default mysql dsn failed: %v", err)
	}
	cfg.DBName = fmt.Sprintf("mini_jupiter_claim_%d", atomic.AddInt64(&testCouponIDSeed, 1))
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

func escapeBackticks(s string) string {
	return strings.ReplaceAll(s, "`", "``")
}

func nextTestCouponID() int64 {
	return atomic.AddInt64(&testCouponIDSeed, 1)
}

func newIntegrationRepository(t *testing.T, db *sql.DB) *Repository {
	t.Helper()
	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	return NewRepository(db, txm)
}

func createCampaign(t *testing.T, db *sql.DB, couponID int64, stock int, perUserLimit int) {
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

func resetTestData(t *testing.T, db *sql.DB, couponID int64) {
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

func countClaimsByUser(t *testing.T, db *sql.DB, couponID, userID int64) int {
	t.Helper()
	var cnt int
	if err := db.QueryRow(`
SELECT COUNT(1)
FROM coupon_claims
WHERE coupon_id = ? AND user_id = ?
`, couponID, userID).Scan(&cnt); err != nil {
		t.Fatalf("count claims by user failed: %v", err)
	}
	return cnt
}

func countClaimsByCoupon(t *testing.T, db *sql.DB, couponID int64) int {
	t.Helper()
	var cnt int
	if err := db.QueryRow(`
SELECT COUNT(1)
FROM coupon_claims
WHERE coupon_id = ?
`, couponID).Scan(&cnt); err != nil {
		t.Fatalf("count claims by coupon failed: %v", err)
	}
	return cnt
}

func loadCampaignStock(t *testing.T, db *sql.DB, couponID int64) int {
	t.Helper()
	var stock int
	if err := db.QueryRow(`
SELECT available_stock
FROM coupon_campaigns
WHERE coupon_id = ?
`, couponID).Scan(&stock); err != nil {
		t.Fatalf("query campaign stock failed: %v", err)
	}
	return stock
}

func openIntegrationRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := strings.TrimSpace(os.Getenv(testRedisAddrEnv))
	fromEnv := true
	if addr == "" {
		addr = defaultTestRedisAddr
		fromEnv = false
	}
	client, err := redis.NewClient(redis.Config{
		Addr:        addr,
		DB:          2,
		DialTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("new redis client failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(context.Background()); err != nil {
		if !fromEnv {
			t.Skipf("skip integration test: %s is not set and docker default redis is unavailable: %v", testRedisAddrEnv, err)
		}
		t.Fatalf("ping redis failed: %v", err)
	}
	if err := client.Raw().FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("flush redis failed: %v", err)
	}
	return client
}

func loadRedisClaimCache(t *testing.T, client *redis.Client, couponID, userID int64) string {
	t.Helper()
	val, err := client.Raw().Get(context.Background(), ClaimCacheKey(couponID, userID)).Result()
	if err != nil {
		if strings.Contains(err.Error(), "redis: nil") {
			return ""
		}
		t.Fatalf("load redis claim cache failed: %v", err)
	}
	return val
}

func resetCouponSchema(t *testing.T, db *sql.DB) {
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
