package claimrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"mini-jupiter/examples/Quan/internal/adjudication/hotpath"
	claimmodel "mini-jupiter/examples/Quan/internal/claim/model"
)

func (r *Repository) FindClaimByID(ctx context.Context, claimID int64) (claimmodel.Record, error) {
	return r.findClaim(ctx, `
SELECT claim_id, coupon_id, user_id, status, idempotency_key, created_at
FROM coupon_claims
WHERE claim_id = ?
LIMIT 1
`, claimID)
}

func (r *Repository) FindClaimByUser(ctx context.Context, couponID, userID int64) (claimmodel.Record, error) {
	return r.findClaim(ctx, `
SELECT claim_id, coupon_id, user_id, status, idempotency_key, created_at
FROM coupon_claims
WHERE coupon_id = ? AND user_id = ?
ORDER BY claim_id DESC
LIMIT 1
`, couponID, userID)
}

func (r *Repository) FindClaimByIdempotency(ctx context.Context, couponID, userID int64, idemKey string) (claimmodel.Record, bool, error) {
	rec, err := r.findClaim(ctx, `
SELECT claim_id, coupon_id, user_id, status, idempotency_key, created_at
FROM coupon_claims
WHERE coupon_id = ? AND user_id = ? AND idempotency_key = ?
LIMIT 1
`, couponID, userID, idemKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return claimmodel.Record{}, false, nil
		}
		return claimmodel.Record{}, false, err
	}
	return rec, true, nil
}

func (r *Repository) loadCampaignForUpdate(ctx context.Context, tx *sql.Tx, couponID int64) (hotpath.CampaignSnapshot, error) {
	var campaign hotpath.CampaignSnapshot
	err := tx.QueryRowContext(ctx, `
SELECT status, available_stock, per_user_limit, start_at, end_at
FROM coupon_campaigns
WHERE coupon_id = ?
FOR UPDATE
`, couponID).Scan(&campaign.Status, &campaign.AvailableStock, &campaign.PerUserLimit, &campaign.StartAt, &campaign.EndAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return hotpath.CampaignSnapshot{}, ErrCampaignNotFound
		}
		return hotpath.CampaignSnapshot{}, fmt.Errorf("query campaign for update: %w", err)
	}
	campaign.CouponID = couponID
	return campaign, nil
}

func (r *Repository) LoadCampaign(ctx context.Context, couponID int64) (hotpath.CampaignSnapshot, error) {
	var campaign hotpath.CampaignSnapshot
	err := r.db.QueryRowContext(ctx, `
SELECT
	status,
	GREATEST(total_stock - (
		SELECT COUNT(1)
		FROM coupon_claims cc
		WHERE cc.coupon_id = coupon_campaigns.coupon_id
	), 0) AS derived_available_stock,
	per_user_limit,
	start_at,
	end_at
FROM coupon_campaigns
WHERE coupon_id = ?
LIMIT 1
`, couponID).Scan(&campaign.Status, &campaign.AvailableStock, &campaign.PerUserLimit, &campaign.StartAt, &campaign.EndAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return hotpath.CampaignSnapshot{}, ErrCampaignNotFound
		}
		return hotpath.CampaignSnapshot{}, fmt.Errorf("query campaign: %w", err)
	}
	campaign.CouponID = couponID
	return campaign, nil
}

func (r *Repository) countUserClaimsTx(ctx context.Context, tx *sql.Tx, couponID, userID int64) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, `
SELECT COUNT(1)
FROM coupon_claims
WHERE coupon_id = ? AND user_id = ?
`, couponID, userID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count user claims: %w", err)
	}
	return count, nil
}

func (r *Repository) findClaimByIdemTx(ctx context.Context, tx *sql.Tx, couponID, userID int64, idemKey string) (claimmodel.Record, bool, error) {
	rec, err := scanClaim(tx.QueryRowContext(ctx, `
SELECT claim_id, coupon_id, user_id, status, idempotency_key, created_at
FROM coupon_claims
WHERE coupon_id = ? AND user_id = ? AND idempotency_key = ?
LIMIT 1
`, couponID, userID, idemKey))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return claimmodel.Record{}, false, nil
		}
		return claimmodel.Record{}, false, fmt.Errorf("query claim by idempotency key for update: %w", err)
	}
	return rec, true, nil
}

func (r *Repository) findClaim(ctx context.Context, query string, args ...any) (claimmodel.Record, error) {
	rec, err := scanClaim(r.db.QueryRowContext(ctx, query, args...))
	if err != nil {
		return claimmodel.Record{}, err
	}
	return rec, nil
}

type claimRow interface {
	Scan(dest ...any) error
}

func scanClaim(row claimRow) (claimmodel.Record, error) {
	var (
		rec  claimmodel.Record
		idem sql.NullString
	)
	err := row.Scan(&rec.ID, &rec.CouponID, &rec.UserID, &rec.Status, &idem, &rec.CreatedAt)
	if err != nil {
		return claimmodel.Record{}, err
	}
	if idem.Valid {
		rec.IdempotencyKey = idem.String
	}
	return rec, nil
}
