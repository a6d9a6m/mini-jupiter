package hotpath

import (
	"context"
	"testing"
	"time"

	"mini-jupiter/examples/Quan/internal/testutil/quanenv"
)

func TestAdjudicator_EnsureCampaign_RepairsIncompleteCampaignState(t *testing.T) {
	redisClient := quanenv.OpenIntegrationRedis(t, 3)
	ctx := context.Background()

	adjudicator := NewAdjudicator(redisClient)
	couponID := quanenv.NextCouponID()
	now := time.Now().UTC()
	campaign := CampaignSnapshot{
		CouponID:       couponID,
		Status:         "ACTIVE",
		AvailableStock: 3,
		PerUserLimit:   1,
		StartAt:        now.Add(-time.Hour),
		EndAt:          now.Add(time.Hour),
	}

	if err := redisClient.Raw().Set(ctx, CampaignStockKey(couponID), campaign.AvailableStock, 0).Err(); err != nil {
		t.Fatalf("seed partial campaign stock failed: %v", err)
	}

	if err := adjudicator.EnsureCampaign(ctx, campaign); err != nil {
		t.Fatalf("ensure campaign failed: %v", err)
	}

	meta, err := redisClient.Raw().HMGet(ctx, CampaignMetaKey(couponID), "status", "start_ms", "end_ms", "per_user_limit").Result()
	if err != nil {
		t.Fatalf("load redis campaign meta failed: %v", err)
	}
	for idx, got := range meta {
		if got == nil {
			t.Fatalf("expected repaired meta field at index %d, got nil", idx)
		}
	}

	decision, err := adjudicator.Decide(ctx, campaign, 94001, "repair-meta", now, "repair-meta-reservation")
	if err != nil {
		t.Fatalf("decide failed after campaign repair: %v", err)
	}
	if decision.Code != DecisionCodeAdmitted {
		t.Fatalf("expected admitted decision after campaign repair, got %q", decision.Code)
	}
}

func TestAdjudicator_WaitResult_WakesOnFinalizePublish(t *testing.T) {
	redisClient := quanenv.OpenIntegrationRedis(t, 3)
	ctx := context.Background()

	adjudicator := NewAdjudicator(redisClient)
	couponID := quanenv.NextCouponID()
	now := time.Now().UTC()
	campaign := CampaignSnapshot{
		CouponID:       couponID,
		Status:         "ACTIVE",
		AvailableStock: 3,
		PerUserLimit:   1,
		StartAt:        now.Add(-time.Hour),
		EndAt:          now.Add(time.Hour),
	}
	if err := adjudicator.EnsureCampaign(ctx, campaign); err != nil {
		t.Fatalf("ensure campaign failed: %v", err)
	}

	decision, err := adjudicator.Decide(ctx, campaign, 94002, "wait-result", now, "wait-result-reservation")
	if err != nil {
		t.Fatalf("decide failed: %v", err)
	}
	if decision.Code != DecisionCodeAdmitted {
		t.Fatalf("expected admitted decision, got %q", decision.Code)
	}

	resultCh := make(chan int64, 1)
	errCh := make(chan error, 1)
	go func() {
		claimID, ok, waitErr := adjudicator.WaitResult(ctx, couponID, 94002, "wait-result")
		if waitErr != nil {
			errCh <- waitErr
			return
		}
		if !ok {
			errCh <- context.DeadlineExceeded
			return
		}
		resultCh <- claimID
	}()

	time.Sleep(50 * time.Millisecond)
	if err := adjudicator.Finalize(ctx, couponID, 94002, "wait-result", decision.ReservationID, 778899); err != nil {
		t.Fatalf("finalize failed: %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatalf("wait result failed: %v", err)
	case claimID := <-resultCh:
		if claimID != 778899 {
			t.Fatalf("expected claim id 778899, got %d", claimID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait result did not wake after finalize publish")
	}
}

func TestAdjudicator_WaitResult_CanceledContextDegradesToNoResult(t *testing.T) {
	redisClient := quanenv.OpenIntegrationRedis(t, 3)
	adjudicator := NewAdjudicator(redisClient)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	claimID, ok, err := adjudicator.WaitResult(ctx, quanenv.NextCouponID(), 94003, "wait-canceled")
	if err != nil {
		t.Fatalf("expected canceled wait to degrade without error, got %v", err)
	}
	if ok {
		t.Fatalf("expected no result for canceled wait, got claim_id=%d", claimID)
	}
	if claimID != 0 {
		t.Fatalf("expected zero claim id for canceled wait, got %d", claimID)
	}
}
