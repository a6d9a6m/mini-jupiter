package coupon

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"mini-jupiter/pkg/mysql"
)

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
		successes   int64
		soldOutErrs int64
		otherErrs   int64
		sampleMu    sync.Mutex
		samples     []string
	)
	runConcurrent(concurrency, func(idx int) {
		userID := int64(30000 + idx)
		_, claimErr := repo.ClaimCoupon(ctx, couponID, userID, "idem-"+strconv.Itoa(idx))
		switch {
		case claimErr == nil:
			atomic.AddInt64(&successes, 1)
		case errors.Is(claimErr, ErrSoldOut):
			atomic.AddInt64(&soldOutErrs, 1)
		default:
			atomic.AddInt64(&otherErrs, 1)
			sampleMu.Lock()
			if len(samples) < 5 {
				samples = append(samples, claimErr.Error())
			}
			sampleMu.Unlock()
		}
	})

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
		successes     int64
		limitErrs     int64
		soldOutErrs   int64
		unexpectedErr int64
		sampleMu      sync.Mutex
		samples       []string
		totalCalls    = userCount * attemptsPerUser
	)

	runConcurrent(totalCalls, func(callIdx int) {
		userOffset := callIdx / attemptsPerUser
		attempt := callIdx % attemptsPerUser
		userID := int64(40000 + userOffset)
		_, claimErr := repo.ClaimCoupon(ctx, couponID, userID, fmt.Sprintf("limit2-%d-%d", userID, attempt))
		switch {
		case claimErr == nil:
			atomic.AddInt64(&successes, 1)
		case errors.Is(claimErr, ErrClaimLimitReached):
			atomic.AddInt64(&limitErrs, 1)
		case errors.Is(claimErr, ErrSoldOut):
			atomic.AddInt64(&soldOutErrs, 1)
		default:
			atomic.AddInt64(&unexpectedErr, 1)
			sampleMu.Lock()
			if len(samples) < 5 {
				samples = append(samples, claimErr.Error())
			}
			sampleMu.Unlock()
		}
	})

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
		successes     int64
		limitErrs     int64
		soldOutErrs   int64
		unexpectedErr int64
		sampleMu      sync.Mutex
		samples       []string
		totalCalls    = userCount * attemptsPerUser
	)

	runConcurrent(totalCalls, func(callIdx int) {
		userOffset := callIdx / attemptsPerUser
		attempt := callIdx % attemptsPerUser
		userID := int64(50000 + userOffset)
		_, claimErr := repo.ClaimCoupon(ctx, couponID, userID, fmt.Sprintf("mixed-%d-%d", userID, attempt))
		switch {
		case claimErr == nil:
			atomic.AddInt64(&successes, 1)
		case errors.Is(claimErr, ErrClaimLimitReached):
			atomic.AddInt64(&limitErrs, 1)
		case errors.Is(claimErr, ErrSoldOut):
			atomic.AddInt64(&soldOutErrs, 1)
		default:
			atomic.AddInt64(&unexpectedErr, 1)
			sampleMu.Lock()
			if len(samples) < 5 {
				samples = append(samples, claimErr.Error())
			}
			sampleMu.Unlock()
		}
	})

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

func runConcurrent(total int, fn func(int)) {
	var wg sync.WaitGroup
	wg.Add(total)
	for i := 0; i < total; i++ {
		go func(idx int) {
			defer wg.Done()
			fn(idx)
		}(i)
	}
	wg.Wait()
}
