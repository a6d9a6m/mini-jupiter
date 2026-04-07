package claimrepo

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	claimmodel "mini-jupiter/examples/Quan/internal/claim/model"
)

func (r *Repository) ClaimCoupon(ctx context.Context, couponID, userID int64, idemKey string) (claimmodel.Record, error) {
	var rec claimmodel.Record
	err := r.txm.WithinTx(ctx, nil, func(ctx context.Context, tx *sql.Tx) error {
		now := time.Now().UTC()
		campaign, err := r.loadCampaignForUpdate(ctx, tx, couponID)
		if err != nil {
			return err
		}
		if campaign.Status != "ACTIVE" || now.Before(campaign.StartAt) || now.After(campaign.EndAt) {
			return ErrCampaignInactive
		}
		if campaign.AvailableStock <= 0 {
			return ErrSoldOut
		}

		if idemKey != "" {
			existing, found, err := r.findClaimByIdemTx(ctx, tx, couponID, userID, idemKey)
			if err != nil {
				return err
			}
			if found {
				rec = existing
				return nil
			}
		}

		userClaimCount, err := r.countUserClaimsTx(ctx, tx, couponID, userID)
		if err != nil {
			return err
		}
		limit := campaign.PerUserLimit
		if limit <= 0 {
			limit = 1
		}
		if userClaimCount >= limit {
			if limit == 1 {
				return ErrAlreadyClaimed
			}
			return ErrClaimLimitReached
		}

		if err := r.deductStock(ctx, tx, couponID, now); err != nil {
			return err
		}

		insertRes, err := tx.ExecContext(ctx, `
INSERT INTO coupon_claims
	(coupon_id, user_id, status, idempotency_key, created_at, updated_at)
VALUES
	(?, ?, 'CLAIMED', NULLIF(?, ''), ?, ?)
`, couponID, userID, idemKey, now, now)
		if err != nil {
			if isDuplicateKey(err) {
				return r.resolveDuplicateClaim(ctx, tx, couponID, userID, idemKey, &rec)
			}
			return fmt.Errorf("insert claim: %w", err)
		}

		claimID, err := insertRes.LastInsertId()
		if err != nil {
			return fmt.Errorf("claim last insert id: %w", err)
		}

		rec = claimmodel.Record{
			ID:             claimID,
			CouponID:       couponID,
			UserID:         userID,
			Status:         "CLAIMED",
			IdempotencyKey: idemKey,
			CreatedAt:      now,
		}
		return nil
	})
	if err != nil {
		return claimmodel.Record{}, err
	}
	return rec, nil
}

func (r *Repository) PersistClaimAfterAdjudication(ctx context.Context, couponID, userID int64, idemKey string) (claimmodel.Record, error) {
	var rec claimmodel.Record
	err := r.txm.WithinTx(ctx, nil, func(ctx context.Context, tx *sql.Tx) error {
		now := time.Now().UTC()
		if idemKey != "" {
			existing, found, err := r.findClaimByIdemTx(ctx, tx, couponID, userID, idemKey)
			if err != nil {
				return err
			}
			if found {
				rec = existing
				return nil
			}
		}

		var (
			existing  bool
			insertErr error
		)
		rec, existing, insertErr = r.insertClaimTx(ctx, tx, couponID, userID, idemKey, now)
		if insertErr != nil {
			return insertErr
		}
		if existing {
			return nil
		}
		if err := r.deductStock(ctx, tx, couponID, now); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return claimmodel.Record{}, err
	}
	return rec, nil
}

func (r *Repository) PersistClaimAsync(ctx context.Context, couponID, userID int64, idemKey string) (claimmodel.Record, bool, error) {
	var (
		rec      claimmodel.Record
		inserted bool
	)
	err := r.txm.WithinTx(ctx, nil, func(ctx context.Context, tx *sql.Tx) error {
		now := time.Now().UTC()
		if idemKey != "" {
			existing, found, err := r.findClaimByIdemTx(ctx, tx, couponID, userID, idemKey)
			if err != nil {
				return err
			}
			if found {
				rec = existing
				inserted = false
				return nil
			}
		}

		created, existing, err := r.insertClaimTx(ctx, tx, couponID, userID, idemKey, now)
		if err != nil {
			return err
		}
		rec = created
		inserted = !existing
		return nil
	})
	if err != nil {
		return claimmodel.Record{}, false, err
	}
	return rec, inserted, nil
}

func (r *Repository) deductStock(ctx context.Context, tx *sql.Tx, couponID int64, now time.Time) error {
	res, err := tx.ExecContext(ctx, `
UPDATE coupon_campaigns
SET available_stock = available_stock - 1, updated_at = ?
WHERE coupon_id = ?
  AND available_stock > 0
`, now, couponID)
	if err != nil {
		return fmt.Errorf("deduct stock: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("deduct stock rows affected: %w", err)
	}
	if affected == 0 {
		return ErrSoldOut
	}
	return nil
}

func (r *Repository) insertClaimTx(ctx context.Context, tx *sql.Tx, couponID, userID int64, idemKey string, now time.Time) (claimmodel.Record, bool, error) {
	insertRes, err := tx.ExecContext(ctx, `
INSERT INTO coupon_claims
	(coupon_id, user_id, status, idempotency_key, created_at, updated_at)
VALUES
	(?, ?, 'CLAIMED', NULLIF(?, ''), ?, ?)
`, couponID, userID, idemKey, now, now)
	if err != nil {
		if isDuplicateKey(err) {
			var existing claimmodel.Record
			resolveErr := r.resolveDuplicateClaim(ctx, tx, couponID, userID, idemKey, &existing)
			if resolveErr != nil {
				return claimmodel.Record{}, false, resolveErr
			}
			return existing, true, nil
		}
		return claimmodel.Record{}, false, fmt.Errorf("insert claim: %w", err)
	}

	claimID, err := insertRes.LastInsertId()
	if err != nil {
		return claimmodel.Record{}, false, fmt.Errorf("claim last insert id: %w", err)
	}

	return claimmodel.Record{
		ID:             claimID,
		CouponID:       couponID,
		UserID:         userID,
		Status:         "CLAIMED",
		IdempotencyKey: idemKey,
		CreatedAt:      now,
	}, false, nil
}

func (r *Repository) resolveDuplicateClaim(ctx context.Context, tx *sql.Tx, couponID, userID int64, idemKey string, rec *claimmodel.Record) error {
	if idemKey != "" {
		existing, found, findErr := r.findClaimByIdemTx(ctx, tx, couponID, userID, idemKey)
		if findErr != nil {
			return findErr
		}
		if found {
			*rec = existing
			return nil
		}
	}
	return ErrAlreadyClaimed
}
