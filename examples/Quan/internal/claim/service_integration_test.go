package claim

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apperr "mini-jupiter/pkg/errors"
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

	repo := newIntegrationRepository(t, db)
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

	repo := newIntegrationRepository(t, db)
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

	repo := newIntegrationRepository(t, db)
	svc := NewService(repo, redisClient, 24*time.Hour)

	if _, err := svc.Claim(ctx, couponID, 93001, "first-key"); err != nil {
		t.Fatalf("first claim failed: %v", err)
	}
	_, err := svc.Claim(ctx, couponID, 93002, "second-key")
	if err == nil {
		t.Fatal("expected sold out error")
	}
	if apperr.HTTPStatus(err) != 409 {
		t.Fatalf("expected conflict status, got %d err=%v", apperr.HTTPStatus(err), err)
	}
}

func TestService_GetMyClaimUsesRedisCacheAsFallback(t *testing.T) {
	db := openIntegrationDB(t)
	redisClient := openIntegrationRedis(t)
	ctx := context.Background()
	couponID := nextTestCouponID()
	resetTestData(t, db, couponID)
	createCampaign(t, db, couponID, 2, 1)

	repo := newIntegrationRepository(t, db)
	svc := NewService(repo, redisClient, 24*time.Hour)

	rec, err := svc.Claim(ctx, couponID, 94001, "claim-cache-key")
	if err != nil {
		t.Fatalf("claim failed: %v", err)
	}
	if got := loadRedisClaimCache(t, redisClient, couponID, 94001); got == "" {
		t.Fatal("expected redis claim cache to be populated after claim")
	}

	if _, err := db.Exec(`DELETE FROM claim_side_effects WHERE claim_id = ?`, rec.ID); err != nil {
		t.Fatalf("delete claim side effects failed: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM coupon_claims WHERE claim_id = ?`, rec.ID); err != nil {
		t.Fatalf("delete claim row failed: %v", err)
	}

	cached, err := svc.GetMyClaim(ctx, couponID, 94001)
	if err != nil {
		t.Fatalf("get my claim from cache failed: %v", err)
	}
	if cached.ID != rec.ID {
		t.Fatalf("expected cached claim id %d, got %d", rec.ID, cached.ID)
	}

	if _, err := repo.FindClaimByUser(ctx, couponID, 94001); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected mysql fallback row to be gone, got err=%v", err)
	}
}
