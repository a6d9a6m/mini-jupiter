package coupon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mini-jupiter/pkg/mysql"

	mysqldriver "github.com/go-sql-driver/mysql"
)

const testMySQLDSNEnv = "QUAN_TEST_MYSQL_DSN"

var testCouponIDSeed int64 = 900000

func TestRepository_PerUserLimitEnforced(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()
	couponID := nextTestCouponID()
	resetTestData(t, db, couponID)
	createCampaign(t, db, couponID, 10, 2)

	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	repo := NewRepository(db, txm, nil, nil, 0)

	if _, err := repo.ClaimCoupon(ctx, couponID, 10001, "k1"); err != nil {
		t.Fatalf("first claim failed: %v", err)
	}
	if _, err := repo.ClaimCoupon(ctx, couponID, 10001, "k2"); err != nil {
		t.Fatalf("second claim failed: %v", err)
	}
	if _, err := repo.ClaimCoupon(ctx, couponID, 10001, "k3"); !errors.Is(err, ErrClaimLimitReached) {
		t.Fatalf("expected ErrClaimLimitReached, got: %v", err)
	}

	if got := countClaimsByUser(t, db, couponID, 10001); got != 2 {
		t.Fatalf("expected user claim count 2, got %d", got)
	}
	if got := loadCampaignStock(t, db, couponID); got != 8 {
		t.Fatalf("expected available stock 8, got %d", got)
	}
}

func TestRepository_IdempotencySameKeyReturnsSameClaim(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()
	couponID := nextTestCouponID()
	resetTestData(t, db, couponID)
	createCampaign(t, db, couponID, 10, 5)

	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	repo := NewRepository(db, txm, nil, nil, 0)

	rec1, err := repo.ClaimCoupon(ctx, couponID, 20001, "same-idem-key")
	if err != nil {
		t.Fatalf("first claim failed: %v", err)
	}
	rec2, err := repo.ClaimCoupon(ctx, couponID, 20001, "same-idem-key")
	if err != nil {
		t.Fatalf("second claim with same idempotency key failed: %v", err)
	}
	if rec1.ID != rec2.ID {
		t.Fatalf("expected same claim id, got first=%d second=%d", rec1.ID, rec2.ID)
	}
	if got := countClaimsByUser(t, db, couponID, 20001); got != 1 {
		t.Fatalf("expected one claim record, got %d", got)
	}
	if got := loadCampaignStock(t, db, couponID); got != 9 {
		t.Fatalf("expected available stock 9, got %d", got)
	}
}

func TestRepository_AlreadyClaimedWhenPerUserLimitIsOne(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()
	couponID := nextTestCouponID()
	resetTestData(t, db, couponID)
	createCampaign(t, db, couponID, 5, 1)

	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	repo := NewRepository(db, txm, nil, nil, 0)

	rec, err := repo.ClaimCoupon(ctx, couponID, 21001, "first-key")
	if err != nil {
		t.Fatalf("first claim failed: %v", err)
	}
	if _, err := repo.ClaimCoupon(ctx, couponID, 21001, "second-key"); !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("expected ErrAlreadyClaimed, got: %v", err)
	}

	if got := countClaimsByUser(t, db, couponID, 21001); got != 1 {
		t.Fatalf("expected one claim record, got %d", got)
	}
	if got := loadCampaignStock(t, db, couponID); got != 4 {
		t.Fatalf("expected available stock 4, got %d", got)
	}

	stored, err := repo.FindClaimByUser(ctx, couponID, 21001)
	if err != nil {
		t.Fatalf("load stored claim failed: %v", err)
	}
	if stored.ID != rec.ID {
		t.Fatalf("expected stored claim id %d, got %d", rec.ID, stored.ID)
	}
}

func TestRepository_ConcurrentReplaySameIdempotencyReturnsSameClaim(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()
	couponID := nextTestCouponID()
	resetTestData(t, db, couponID)
	createCampaign(t, db, couponID, 10, 1)

	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	repo := NewRepository(db, txm, nil, nil, 0)

	const concurrency = 32
	var (
		wg         sync.WaitGroup
		errCount   int64
		unexpected []string
		sampleMu   sync.Mutex
		claimIDs   = make(chan int64, concurrency)
	)

	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			rec, err := repo.ClaimCoupon(ctx, couponID, 22001, "replay-key")
			if err != nil {
				atomic.AddInt64(&errCount, 1)
				sampleMu.Lock()
				if len(unexpected) < 5 {
					unexpected = append(unexpected, err.Error())
				}
				sampleMu.Unlock()
				return
			}
			claimIDs <- rec.ID
		}()
	}
	wg.Wait()
	close(claimIDs)

	if errCount != 0 {
		t.Fatalf("expected no errors, got %d samples=%v", errCount, unexpected)
	}

	var firstID int64
	for claimID := range claimIDs {
		if firstID == 0 {
			firstID = claimID
			continue
		}
		if claimID != firstID {
			t.Fatalf("expected all replayed calls to return claim id %d, got %d", firstID, claimID)
		}
	}
	if firstID == 0 {
		t.Fatal("expected at least one claim id result")
	}

	if got := countClaimsByUser(t, db, couponID, 22001); got != 1 {
		t.Fatalf("expected one persisted claim record, got %d", got)
	}
	if got := loadCampaignStock(t, db, couponID); got != 9 {
		t.Fatalf("expected available stock 9, got %d", got)
	}
}

func TestRepository_SoldOutAfterStockExhausted(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()
	couponID := nextTestCouponID()
	resetTestData(t, db, couponID)
	createCampaign(t, db, couponID, 1, 1)

	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	repo := NewRepository(db, txm, nil, nil, 0)

	if _, err := repo.ClaimCoupon(ctx, couponID, 23001, "first"); err != nil {
		t.Fatalf("first claim failed: %v", err)
	}
	if _, err := repo.ClaimCoupon(ctx, couponID, 23002, "second"); !errors.Is(err, ErrSoldOut) {
		t.Fatalf("expected ErrSoldOut, got: %v", err)
	}

	if got := countClaimsByCoupon(t, db, couponID); got != 1 {
		t.Fatalf("expected one claim record, got %d", got)
	}
	if got := loadCampaignStock(t, db, couponID); got != 0 {
		t.Fatalf("expected available stock 0, got %d", got)
	}
}

func TestRepository_ConcurrentClaimNoOversell(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()
	couponID := nextTestCouponID()
	resetTestData(t, db, couponID)

	const (
		stock       = 20
		concurrency = 200
	)
	createCampaign(t, db, couponID, stock, 1)

	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	repo := NewRepository(db, txm, nil, nil, 0)

	var (
		wg          sync.WaitGroup
		successes   int64
		soldOutErrs int64
		otherErrs   int64
		sampleMu    sync.Mutex
		samples     []string
	)
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		idx := i
		go func() {
			defer wg.Done()
			userID := int64(30000 + idx)
			_, err := repo.ClaimCoupon(ctx, couponID, userID, "idem-"+strconv.Itoa(idx))
			switch {
			case err == nil:
				atomic.AddInt64(&successes, 1)
			case errors.Is(err, ErrSoldOut):
				atomic.AddInt64(&soldOutErrs, 1)
			default:
				atomic.AddInt64(&otherErrs, 1)
				sampleMu.Lock()
				if len(samples) < 5 {
					samples = append(samples, err.Error())
				}
				sampleMu.Unlock()
			}
		}()
	}
	wg.Wait()

	if otherErrs != 0 {
		t.Fatalf("expected no unexpected errors, got %d samples=%v", otherErrs, samples)
	}
	if successes != stock {
		t.Fatalf("expected success=%d, got %d (soldOut=%d)", stock, successes, soldOutErrs)
	}
	remaining := loadCampaignStock(t, db, couponID)
	if remaining < 0 {
		t.Fatalf("expected non-negative stock, got %d", remaining)
	}
	if remaining != 0 {
		t.Fatalf("expected remaining stock 0, got %d", remaining)
	}
	totalClaims := countClaimsByCoupon(t, db, couponID)
	if totalClaims != int(successes) {
		t.Fatalf("expected claim count %d, got %d", successes, totalClaims)
	}
}

func TestRepository_ConcurrentMultiUserLimitTwoNoOverflow(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()
	couponID := nextTestCouponID()
	resetTestData(t, db, couponID)

	const (
		userCount       = 80
		attemptsPerUser = 20
		perUserLimit    = 2
		stock           = 200
	)
	createCampaign(t, db, couponID, stock, perUserLimit)

	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	repo := NewRepository(db, txm, nil, nil, 0)

	var (
		wg            sync.WaitGroup
		successes     int64
		limitErrs     int64
		soldOutErrs   int64
		unexpectedErr int64
		sampleMu      sync.Mutex
		samples       []string
	)
	for u := 0; u < userCount; u++ {
		userID := int64(40000 + u)
		for a := 0; a < attemptsPerUser; a++ {
			attempt := a
			wg.Add(1)
			go func(uid int64) {
				defer wg.Done()
				_, err := repo.ClaimCoupon(ctx, couponID, uid, fmt.Sprintf("limit2-%d-%d", uid, attempt))
				switch {
				case err == nil:
					atomic.AddInt64(&successes, 1)
				case errors.Is(err, ErrClaimLimitReached):
					atomic.AddInt64(&limitErrs, 1)
				case errors.Is(err, ErrSoldOut):
					atomic.AddInt64(&soldOutErrs, 1)
				default:
					atomic.AddInt64(&unexpectedErr, 1)
					sampleMu.Lock()
					if len(samples) < 5 {
						samples = append(samples, err.Error())
					}
					sampleMu.Unlock()
				}
			}(userID)
		}
	}
	wg.Wait()

	if unexpectedErr != 0 {
		t.Fatalf("expected no unexpected errors, got %d samples=%v", unexpectedErr, samples)
	}
	if soldOutErrs != 0 {
		t.Fatalf("expected sold out errors = 0 because stock is sufficient, got %d", soldOutErrs)
	}
	expectedSuccess := int64(userCount * perUserLimit)
	if successes != expectedSuccess {
		t.Fatalf("expected successes=%d, got %d (limitErrs=%d)", expectedSuccess, successes, limitErrs)
	}
	totalClaims := countClaimsByCoupon(t, db, couponID)
	if totalClaims != int(expectedSuccess) {
		t.Fatalf("expected total claims %d, got %d", expectedSuccess, totalClaims)
	}
	for u := 0; u < userCount; u++ {
		userID := int64(40000 + u)
		got := countClaimsByUser(t, db, couponID, userID)
		if got > perUserLimit {
			t.Fatalf("expected user %d claims <= %d, got %d", userID, perUserLimit, got)
		}
		if got != perUserLimit {
			t.Fatalf("expected user %d claims = %d, got %d", userID, perUserLimit, got)
		}
	}
	expectedRemaining := stock - int(expectedSuccess)
	remaining := loadCampaignStock(t, db, couponID)
	if remaining != expectedRemaining {
		t.Fatalf("expected remaining stock=%d, got %d", expectedRemaining, remaining)
	}
}

func TestRepository_ConcurrentStockAndLimitMixedNoOverflow(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()
	couponID := nextTestCouponID()
	resetTestData(t, db, couponID)

	const (
		userCount       = 100
		attemptsPerUser = 10
		perUserLimit    = 2
		stock           = 120
	)
	createCampaign(t, db, couponID, stock, perUserLimit)

	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	repo := NewRepository(db, txm, nil, nil, 0)

	var (
		wg            sync.WaitGroup
		successes     int64
		limitErrs     int64
		soldOutErrs   int64
		unexpectedErr int64
		sampleMu      sync.Mutex
		samples       []string
	)
	for u := 0; u < userCount; u++ {
		userID := int64(50000 + u)
		for a := 0; a < attemptsPerUser; a++ {
			attempt := a
			wg.Add(1)
			go func(uid int64) {
				defer wg.Done()
				_, err := repo.ClaimCoupon(ctx, couponID, uid, fmt.Sprintf("mixed-%d-%d", uid, attempt))
				switch {
				case err == nil:
					atomic.AddInt64(&successes, 1)
				case errors.Is(err, ErrClaimLimitReached):
					atomic.AddInt64(&limitErrs, 1)
				case errors.Is(err, ErrSoldOut):
					atomic.AddInt64(&soldOutErrs, 1)
				default:
					atomic.AddInt64(&unexpectedErr, 1)
					sampleMu.Lock()
					if len(samples) < 5 {
						samples = append(samples, err.Error())
					}
					sampleMu.Unlock()
				}
			}(userID)
		}
	}
	wg.Wait()

	if unexpectedErr != 0 {
		t.Fatalf("expected no unexpected errors, got %d samples=%v", unexpectedErr, samples)
	}
	if successes != stock {
		t.Fatalf("expected successes=%d due to stock bottleneck, got %d (limitErrs=%d soldOutErrs=%d)", stock, successes, limitErrs, soldOutErrs)
	}
	totalClaims := countClaimsByCoupon(t, db, couponID)
	if totalClaims != stock {
		t.Fatalf("expected total claims=%d, got %d", stock, totalClaims)
	}
	for u := 0; u < userCount; u++ {
		userID := int64(50000 + u)
		got := countClaimsByUser(t, db, couponID, userID)
		if got > perUserLimit {
			t.Fatalf("expected user %d claims <= %d, got %d", userID, perUserLimit, got)
		}
	}
	remaining := loadCampaignStock(t, db, couponID)
	if remaining != 0 {
		t.Fatalf("expected remaining stock=0, got %d", remaining)
	}
}

func openIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(testMySQLDSNEnv))
	if dsn == "" {
		t.Skipf("skip integration test: %s is not set", testMySQLDSNEnv)
	}
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
