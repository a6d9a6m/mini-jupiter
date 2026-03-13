package coupon

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"mini-jupiter/pkg/mysql"
)

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
		errCount   int64
		unexpected []string
		sampleMu   sync.Mutex
		claimIDs   = make(chan int64, concurrency)
	)

	runConcurrent(concurrency, func(_ int) {
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
	})
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
