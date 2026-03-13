package coupon

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apperr "mini-jupiter/pkg/errors"
	"mini-jupiter/pkg/mysql"
)

const testRedisAddrEnv = "QUAN_TEST_REDIS_ADDR"
const defaultTestRedisAddr = "127.0.0.1:6379"

func TestService_ConcurrentReplaySameIdempotencyUsesRedisReuse(t *testing.T) {
	db := openIntegrationDB(t)
	redisClient := openIntegrationRedis(t)
	ctx := context.Background()
	couponID := nextTestCouponID()
	resetTestData(t, db, couponID)
	createCampaign(t, db, couponID, 10, 1)

	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	repo := NewRepository(db, txm, nil, nil, 0)
	svc := NewService(repo, redisClient, 24*time.Hour)

	const concurrency = 32
	var (
		errCount   int64
		unexpected []string
		sampleMu   sync.Mutex
		claimIDs   = make(chan int64, concurrency)
	)

	runConcurrent(concurrency, func(_ int) {
		rec, claimErr := svc.Claim(ctx, couponID, 91001, "replay-key")
		if claimErr != nil {
			atomic.AddInt64(&errCount, 1)
			sampleMu.Lock()
			if len(unexpected) < 5 {
				unexpected = append(unexpected, claimErr.Error())
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
	if got := countClaimsByUser(t, db, couponID, 91001); got != 1 {
		t.Fatalf("expected one persisted claim record, got %d", got)
	}
	if got := loadCampaignStock(t, db, couponID); got != 9 {
		t.Fatalf("expected available stock 9, got %d", got)
	}
}

func TestService_DifferentIdempotencySameUserStillConflicts(t *testing.T) {
	db := openIntegrationDB(t)
	redisClient := openIntegrationRedis(t)
	ctx := context.Background()
	couponID := nextTestCouponID()
	resetTestData(t, db, couponID)
	createCampaign(t, db, couponID, 5, 1)

	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	repo := NewRepository(db, txm, nil, nil, 0)
	svc := NewService(repo, redisClient, 24*time.Hour)

	rec, err := svc.Claim(ctx, couponID, 92001, "first-key")
	if err != nil {
		t.Fatalf("first claim failed: %v", err)
	}
	if rec.ID <= 0 {
		t.Fatalf("expected persisted claim id, got %d", rec.ID)
	}

	_, err = svc.Claim(ctx, couponID, 92001, "second-key")
	if err == nil {
		t.Fatal("expected second claim to conflict")
	}
	if apperr.HTTPStatus(err) != 409 {
		t.Fatalf("expected conflict status, got %d err=%v", apperr.HTTPStatus(err), err)
	}
}

func TestService_SoldOutConflictFromRedisHotPath(t *testing.T) {
	db := openIntegrationDB(t)
	redisClient := openIntegrationRedis(t)
	ctx := context.Background()
	couponID := nextTestCouponID()
	resetTestData(t, db, couponID)
	createCampaign(t, db, couponID, 1, 1)

	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	repo := NewRepository(db, txm, nil, nil, 0)
	svc := NewService(repo, redisClient, 24*time.Hour)

	if _, err := svc.Claim(ctx, couponID, 93001, "first-key"); err != nil {
		t.Fatalf("first claim failed: %v", err)
	}
	_, err = svc.Claim(ctx, couponID, 93002, "second-key")
	if err == nil {
		t.Fatal("expected sold out error")
	}
	if apperr.HTTPStatus(err) != 409 {
		t.Fatalf("expected conflict status, got %d err=%v", apperr.HTTPStatus(err), err)
	}
}
