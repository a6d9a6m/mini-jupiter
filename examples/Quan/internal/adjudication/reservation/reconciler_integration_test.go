package reservation

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"mini-jupiter/examples/Quan/internal/adjudication/hotpath"
	claimrepo "mini-jupiter/examples/Quan/internal/claim/repository"
	"mini-jupiter/examples/Quan/internal/testutil/quanenv"
	"mini-jupiter/pkg/mysql"
)

func TestReservationReconciler_RollsBackExpiredLeaseWithoutPersistedClaim(t *testing.T) {
	db := quanenv.OpenIntegrationDB(t, "reservation")
	redisClient := quanenv.OpenIntegrationRedis(t, 4)
	ctx := context.Background()
	couponID := quanenv.NextCouponID()
	quanenv.ResetTestData(t, db, couponID)
	quanenv.CreateCampaign(t, db, couponID, 3, 1)

	repo := newIntegrationRepository(t, db)
	adjudicator := hotpath.NewAdjudicator(redisClient)
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
	if decision.Code != hotpath.DecisionCodeAdmitted {
		t.Fatalf("expected admitted decision, got %s", decision.Code)
	}

	if got := quanenv.LoadRedisCampaignStock(t, redisClient, couponID, hotpath.CampaignStockKey); got != 2 {
		t.Fatalf("expected redis stock 2 after reservation, got %d", got)
	}
	if got := quanenv.LoadRedisUserCount(t, redisClient, couponID, 94001, hotpath.CampaignUserCountKey); got != 1 {
		t.Fatalf("expected redis user count 1 after reservation, got %d", got)
	}
	if got := quanenv.LoadRedisString(t, redisClient, hotpath.IdemDecisionKey(couponID, 94001, "lease-rollback")); got != "PENDING:"+decision.ReservationID {
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
	if got := quanenv.LoadRedisCampaignStock(t, redisClient, couponID, hotpath.CampaignStockKey); got != 3 {
		t.Fatalf("expected redis stock restored to 3, got %d", got)
	}
	if got := quanenv.LoadRedisUserCount(t, redisClient, couponID, 94001, hotpath.CampaignUserCountKey); got != 0 {
		t.Fatalf("expected redis user count restored to 0, got %d", got)
	}
	if got := quanenv.LoadRedisString(t, redisClient, hotpath.IdemDecisionKey(couponID, 94001, "lease-rollback")); got != "" {
		t.Fatalf("expected idem key cleared after rollback, got %q", got)
	}
	if got := quanenv.LoadRedisHashField(t, redisClient, hotpath.ReservationLeaseKey(decision.ReservationID), "state"); got != "ROLLED_BACK" {
		t.Fatalf("expected lease state ROLLED_BACK, got %q", got)
	}
	if leases, err := adjudicator.ListExpiredReservations(ctx, now.Add(time.Minute), 10); err != nil {
		t.Fatalf("list expired reservations failed: %v", err)
	} else if len(leases) != 0 {
		t.Fatalf("expected no expired reservations after rollback, got %d", len(leases))
	}
}

func TestReservationReconciler_FinalizesExpiredLeaseWhenClaimWasPersisted(t *testing.T) {
	db := quanenv.OpenIntegrationDB(t, "reservation")
	redisClient := quanenv.OpenIntegrationRedis(t, 4)
	ctx := context.Background()
	couponID := quanenv.NextCouponID()
	quanenv.ResetTestData(t, db, couponID)
	quanenv.CreateCampaign(t, db, couponID, 3, 1)

	repo := newIntegrationRepository(t, db)
	adjudicator := hotpath.NewAdjudicator(redisClient)
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
	if decision.Code != hotpath.DecisionCodeAdmitted {
		t.Fatalf("expected admitted decision, got %s", decision.Code)
	}

	rec, err := repo.PersistClaimAfterAdjudication(ctx, couponID, 95001, "lease-finalize")
	if err != nil {
		t.Fatalf("persist claim after adjudication failed: %v", err)
	}
	if rec.ID <= 0 {
		t.Fatalf("expected persisted claim id, got %d", rec.ID)
	}
	if got := quanenv.LoadRedisString(t, redisClient, hotpath.IdemDecisionKey(couponID, 95001, "lease-finalize")); got != "PENDING:"+decision.ReservationID {
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

	if got := quanenv.LoadRedisString(t, redisClient, hotpath.IdemDecisionKey(couponID, 95001, "lease-finalize")); got != fmt.Sprintf("SUCCESS:%d:%s", rec.ID, decision.ReservationID) {
		t.Fatalf("expected success idem value after reconcile, got %q", got)
	}
	if got := quanenv.LoadRedisCampaignStock(t, redisClient, couponID, hotpath.CampaignStockKey); got != 2 {
		t.Fatalf("expected redis stock to stay reserved at 2, got %d", got)
	}
	if got := quanenv.LoadRedisUserCount(t, redisClient, couponID, 95001, hotpath.CampaignUserCountKey); got != 1 {
		t.Fatalf("expected redis user count to stay 1, got %d", got)
	}
	if got := quanenv.LoadRedisHashField(t, redisClient, hotpath.ReservationLeaseKey(decision.ReservationID), "state"); got != "FINALIZED" {
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

func newIntegrationRepository(t *testing.T, db *sql.DB) *claimrepo.Repository {
	t.Helper()
	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	return claimrepo.NewRepository(db, txm)
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
