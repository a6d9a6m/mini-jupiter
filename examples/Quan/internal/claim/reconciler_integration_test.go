package claim

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestReservationReconciler_RollsBackExpiredLeaseWithoutPersistedClaim(t *testing.T) {
	db := openIntegrationDB(t)
	redisClient := openIntegrationRedis(t)
	ctx := context.Background()
	couponID := nextTestCouponID()
	resetTestData(t, db, couponID)
	createCampaign(t, db, couponID, 3, 1)

	repo := newIntegrationRepository(t, db)
	adjudicator := NewAdjudicator(redisClient)
	adjudicator.SetLeaseTTL(50 * time.Millisecond)

	campaign, err := repo.LoadCampaign(ctx, couponID)
	if err != nil {
		t.Fatalf("load campaign failed: %v", err)
	}
	if err := adjudicator.EnsureCampaign(ctx, campaign); err != nil {
		t.Fatalf("ensure campaign failed: %v", err)
	}

	now := time.Now().UTC()
	decision, err := adjudicator.Decide(ctx, campaign, 94001, "lease-rollback", now, "lease-rollback-1")
	if err != nil {
		t.Fatalf("redis decide failed: %v", err)
	}
	if decision.Code != decisionCodeAdmitted {
		t.Fatalf("expected admitted decision, got %s", decision.Code)
	}

	if got := loadRedisCampaignStock(t, redisClient, couponID); got != 2 {
		t.Fatalf("expected redis stock 2 after reservation, got %d", got)
	}
	if got := loadRedisUserCount(t, redisClient, couponID, 94001); got != 1 {
		t.Fatalf("expected redis user count 1 after reservation, got %d", got)
	}
	if got := loadRedisIdemValue(t, redisClient, couponID, 94001, "lease-rollback"); got != "PENDING:"+decision.ReservationID {
		t.Fatalf("expected pending idem value, got %q", got)
	}

	reconciler, err := NewReservationReconciler(repo, adjudicator, ReservationReconcilerConfig{
		Enabled:   true,
		BatchSize: 10,
	})
	if err != nil {
		t.Fatalf("new reservation reconciler failed: %v", err)
	}
	if err := reconciler.ReconcileOnce(ctx, now.Add(adjudicator.LeaseTTL()+20*time.Millisecond)); err != nil {
		t.Fatalf("reconcile once failed: %v", err)
	}

	if _, found, err := repo.FindClaimByIdempotency(ctx, couponID, 94001, "lease-rollback"); err != nil {
		t.Fatalf("find claim by idempotency failed: %v", err)
	} else if found {
		t.Fatal("expected no persisted claim after rollback reconciliation")
	}
	if got := loadRedisCampaignStock(t, redisClient, couponID); got != 3 {
		t.Fatalf("expected redis stock restored to 3, got %d", got)
	}
	if got := loadRedisUserCount(t, redisClient, couponID, 94001); got != 0 {
		t.Fatalf("expected redis user count restored to 0, got %d", got)
	}
	if got := loadRedisIdemValue(t, redisClient, couponID, 94001, "lease-rollback"); got != "" {
		t.Fatalf("expected idem key cleared after rollback, got %q", got)
	}
	if got := loadReservationLeaseState(t, redisClient, decision.ReservationID); got != "ROLLED_BACK" {
		t.Fatalf("expected lease state ROLLED_BACK, got %q", got)
	}
	if leases, err := adjudicator.ListExpiredReservations(ctx, now.Add(time.Minute), 10); err != nil {
		t.Fatalf("list expired reservations failed: %v", err)
	} else if len(leases) != 0 {
		t.Fatalf("expected no expired reservations after rollback, got %d", len(leases))
	}
}

func TestReservationReconciler_FinalizesExpiredLeaseWhenClaimWasPersisted(t *testing.T) {
	db := openIntegrationDB(t)
	redisClient := openIntegrationRedis(t)
	ctx := context.Background()
	couponID := nextTestCouponID()
	resetTestData(t, db, couponID)
	createCampaign(t, db, couponID, 3, 1)

	repo := newIntegrationRepository(t, db)
	adjudicator := NewAdjudicator(redisClient)
	adjudicator.SetLeaseTTL(50 * time.Millisecond)

	campaign, err := repo.LoadCampaign(ctx, couponID)
	if err != nil {
		t.Fatalf("load campaign failed: %v", err)
	}
	if err := adjudicator.EnsureCampaign(ctx, campaign); err != nil {
		t.Fatalf("ensure campaign failed: %v", err)
	}

	now := time.Now().UTC()
	decision, err := adjudicator.Decide(ctx, campaign, 95001, "lease-finalize", now, "lease-finalize-1")
	if err != nil {
		t.Fatalf("redis decide failed: %v", err)
	}
	if decision.Code != decisionCodeAdmitted {
		t.Fatalf("expected admitted decision, got %s", decision.Code)
	}

	rec, err := repo.PersistClaimAfterAdjudication(ctx, couponID, 95001, "lease-finalize")
	if err != nil {
		t.Fatalf("persist claim after adjudication failed: %v", err)
	}
	if rec.ID <= 0 {
		t.Fatalf("expected persisted claim id, got %d", rec.ID)
	}
	if got := loadRedisIdemValue(t, redisClient, couponID, 95001, "lease-finalize"); got != "PENDING:"+decision.ReservationID {
		t.Fatalf("expected pending idem value before reconcile, got %q", got)
	}

	reconciler, err := NewReservationReconciler(repo, adjudicator, ReservationReconcilerConfig{
		Enabled:   true,
		BatchSize: 10,
	})
	if err != nil {
		t.Fatalf("new reservation reconciler failed: %v", err)
	}
	if err := reconciler.ReconcileOnce(ctx, now.Add(adjudicator.LeaseTTL()+20*time.Millisecond)); err != nil {
		t.Fatalf("reconcile once failed: %v", err)
	}

	if got := loadRedisIdemValue(t, redisClient, couponID, 95001, "lease-finalize"); got != fmt.Sprintf("SUCCESS:%d", rec.ID) {
		t.Fatalf("expected success idem value after reconcile, got %q", got)
	}
	if got := loadRedisCampaignStock(t, redisClient, couponID); got != 2 {
		t.Fatalf("expected redis stock to stay reserved at 2, got %d", got)
	}
	if got := loadRedisUserCount(t, redisClient, couponID, 95001); got != 1 {
		t.Fatalf("expected redis user count to stay 1, got %d", got)
	}
	if got := loadReservationLeaseState(t, redisClient, decision.ReservationID); got != "FINALIZED" {
		t.Fatalf("expected lease state FINALIZED, got %q", got)
	}
	if got := countClaimsByUser(t, db, couponID, 95001); got != 1 {
		t.Fatalf("expected one persisted claim, got %d", got)
	}
	if got := loadCampaignStock(t, db, couponID); got != 2 {
		t.Fatalf("expected mysql stock 2 after persisted claim, got %d", got)
	}
	if leases, err := adjudicator.ListExpiredReservations(ctx, now.Add(time.Minute), 10); err != nil {
		t.Fatalf("list expired reservations failed: %v", err)
	} else if len(leases) != 0 {
		t.Fatalf("expected no expired reservations after finalize, got %d", len(leases))
	}
}
