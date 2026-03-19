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
	ensureDatabaseExists(t, dsn)

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

func ensureDatabaseExists(t *testing.T, dsn string) {
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
	createDBSQL := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", escapeBackticks(targetDB))
	if _, err := adminDB.ExecContext(ctx, createDBSQL); err != nil {
		t.Fatalf("create test database failed: %v", err)
	}
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
	return NewRepository(db, txm, NewSideEffectRepository(db))
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

func countAsyncTasksTotal(t *testing.T, db *sql.DB) int {
	t.Helper()
	var cnt int
	if err := db.QueryRow(`SELECT COUNT(1) FROM async_tasks`).Scan(&cnt); err != nil {
		t.Fatalf("count async tasks failed: %v", err)
	}
	return cnt
}

func countOutboxEventsTotal(t *testing.T, db *sql.DB) int {
	t.Helper()
	var cnt int
	if err := db.QueryRow(`SELECT COUNT(1) FROM outbox_events`).Scan(&cnt); err != nil {
		t.Fatalf("count outbox events failed: %v", err)
	}
	return cnt
}

func loadClaimSideEffectByClaim(t *testing.T, db *sql.DB, claimID int64) ClaimSideEffect {
	t.Helper()
	rec, err := scanClaimSideEffect(db.QueryRow(`
SELECT side_effect_id, claim_id, effect_type, payload_json, status, retry_count, last_error,
       COALESCE(async_task_id, 0), COALESCE(outbox_event_id, 0), created_at, updated_at
FROM claim_side_effects
WHERE claim_id = ? AND effect_type = ?
LIMIT 1
`, claimID, ClaimSideEffectTypeClaimCreated))
	if err != nil {
		t.Fatalf("load claim side effect failed: %v", err)
	}
	return rec
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

func loadRedisCampaignStock(t *testing.T, client *redis.Client, couponID int64) int {
	t.Helper()
	val, err := client.Raw().Get(context.Background(), campaignStockKey(couponID)).Int()
	if err != nil {
		t.Fatalf("load redis campaign stock failed: %v", err)
	}
	return val
}

func loadRedisUserCount(t *testing.T, client *redis.Client, couponID, userID int64) int {
	t.Helper()
	val, err := client.Raw().HGet(context.Background(), campaignUserCountKey(couponID), fmt.Sprintf("%d", userID)).Int()
	if err != nil {
		if strings.Contains(err.Error(), "redis: nil") {
			return 0
		}
		t.Fatalf("load redis user count failed: %v", err)
	}
	return val
}

func loadRedisIdemValue(t *testing.T, client *redis.Client, couponID, userID int64, idemKey string) string {
	t.Helper()
	val, err := client.Raw().Get(context.Background(), idemDecisionKey(couponID, userID, idemKey)).Result()
	if err != nil {
		if strings.Contains(err.Error(), "redis: nil") {
			return ""
		}
		t.Fatalf("load redis idem value failed: %v", err)
	}
	return val
}

func loadReservationLeaseState(t *testing.T, client *redis.Client, reservationID string) string {
	t.Helper()
	val, err := client.Raw().HGet(context.Background(), reservationLeaseKey(reservationID), "state").Result()
	if err != nil {
		if strings.Contains(err.Error(), "redis: nil") {
			return ""
		}
		t.Fatalf("load reservation lease state failed: %v", err)
	}
	return val
}

func loadRedisClaimCache(t *testing.T, client *redis.Client, couponID, userID int64) string {
	t.Helper()
	val, err := client.Raw().Get(context.Background(), claimCacheKey(couponID, userID)).Result()
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
