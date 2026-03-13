package coupon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"mini-jupiter/examples/Quan/internal/outbox"
	"mini-jupiter/examples/Quan/internal/task"
	applog "mini-jupiter/pkg/log"
)

func (r *Repository) ClaimCoupon(ctx context.Context, couponID, userID int64, idemKey string) (ClaimRecord, error) {
	var rec ClaimRecord
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

		rec = ClaimRecord{
			ID:             claimID,
			CouponID:       couponID,
			UserID:         userID,
			Status:         "CLAIMED",
			IdempotencyKey: idemKey,
			CreatedAt:      now,
		}
		return r.createTaskAndOutbox(ctx, tx, rec)
	})
	if err != nil {
		return ClaimRecord{}, err
	}
	return rec, nil
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

func (r *Repository) resolveDuplicateClaim(ctx context.Context, tx *sql.Tx, couponID, userID int64, idemKey string, rec *ClaimRecord) error {
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

func (r *Repository) createTaskAndOutbox(ctx context.Context, tx *sql.Tx, rec ClaimRecord) error {
	if r.taskRepo == nil || r.outboxRepo == nil {
		return nil
	}
	payload, err := task.MarshalPayload(task.SendCouponNoticePayload{
		ClaimID:  rec.ID,
		CouponID: rec.CouponID,
		UserID:   rec.UserID,
		TraceID:  applog.TraceIDFromContext(ctx),
	})
	if err != nil {
		return fmt.Errorf("marshal task payload: %w", err)
	}
	bizID := fmt.Sprintf("claim:%d", rec.ID)
	asyncTask, err := r.taskRepo.CreateTx(ctx, tx, task.CreateTaskParams{
		TaskType: task.TaskTypeSendCouponNotice,
		BizID:    bizID,
		Payload:  payload,
		MaxRetry: r.defaultTaskMaxRetry,
	})
	if err != nil {
		if errors.Is(err, task.ErrTaskDuplicate) {
			existing, queryErr := r.taskRepo.GetByTypeBiz(ctx, task.TaskTypeSendCouponNotice, bizID)
			if queryErr != nil {
				return fmt.Errorf("query duplicated async task: %w", queryErr)
			}
			asyncTask = existing
		} else {
			return fmt.Errorf("create async task in claim tx: %w", err)
		}
	}
	eventPayload, err := outbox.MarshalTaskCreatedPayload(asyncTask.ID)
	if err != nil {
		return fmt.Errorf("marshal outbox event payload: %w", err)
	}
	_, err = r.outboxRepo.CreateTx(ctx, tx, outbox.CreateEventParams{
		EventType:     outbox.EventTypeTaskCreated,
		AggregateType: "async_task",
		AggregateID:   strconv.FormatInt(asyncTask.ID, 10),
		PayloadJSON:   eventPayload,
	})
	if err != nil {
		return fmt.Errorf("create outbox event in claim tx: %w", err)
	}
	return nil
}
