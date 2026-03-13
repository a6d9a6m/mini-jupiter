package coupon

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

	mysqldriver "github.com/go-sql-driver/mysql"
)

const testMySQLDSNEnv = "QUAN_TEST_MYSQL_DSN"
const defaultTestMySQLDSN = "root:root@tcp(127.0.0.1:3306)/mini_jupiter?parseTime=true&loc=Local&charset=utf8mb4"

var testCouponIDSeed int64 = 900000

func openIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn, fromEnv := resolveTestMySQLDSN()
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

func resolveTestMySQLDSN() (string, bool) {
	dsn := strings.TrimSpace(os.Getenv(testMySQLDSNEnv))
	if dsn != "" {
		return dsn, true
	}
	return defaultTestMySQLDSN, false
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
