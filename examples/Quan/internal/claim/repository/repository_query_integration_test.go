package claimrepo

import (
	"context"
	"testing"
	"time"

	"mini-jupiter/examples/Quan/internal/testutil/quanenv"
	"mini-jupiter/pkg/mysql"
)

func TestRepository_LoadCampaignDerivesAvailableStockFromClaimLedger(t *testing.T) {
	db := quanenv.OpenIntegrationDB(t, "claimrepo")
	ctx := context.Background()
	couponID := quanenv.NextCouponID()
	quanenv.ResetTestData(t, db, couponID)
	quanenv.CreateCampaign(t, db, couponID, 5, 1)

	if _, err := db.ExecContext(ctx, `
UPDATE coupon_campaigns
SET available_stock = 5
WHERE coupon_id = ?
`, couponID); err != nil {
		t.Fatalf("reset mysql available_stock failed: %v", err)
	}
	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `
INSERT INTO coupon_claims
	(coupon_id, user_id, status, idempotency_key, created_at, updated_at)
VALUES
	(?, ?, 'CLAIMED', ?, ?, ?),
	(?, ?, 'CLAIMED', ?, ?, ?)
`, couponID, 99001, "ledger-1", now, now, couponID, 99002, "ledger-2", now, now); err != nil {
		t.Fatalf("seed coupon claims failed: %v", err)
	}

	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	repo := NewRepository(db, txm)

	campaign, err := repo.LoadCampaign(ctx, couponID)
	if err != nil {
		t.Fatalf("load campaign failed: %v", err)
	}
	if campaign.AvailableStock != 3 {
		t.Fatalf("expected derived available stock 3, got %d", campaign.AvailableStock)
	}
}
