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
	"mini-jupiter/pkg/mysql"

	mysqlerr "github.com/go-sql-driver/mysql"
)

var (
	ErrCampaignNotFound  = errors.New("campaign not found")
	ErrCampaignInactive  = errors.New("campaign not active")
	ErrSoldOut           = errors.New("coupon sold out")
	ErrAlreadyClaimed    = errors.New("already claimed")
	ErrClaimLimitReached = errors.New("claim limit reached")
)

type Repository struct {
	db                  *sql.DB
	txm                 *mysql.TxManager
	taskRepo            *task.Repository
	outboxRepo          *outbox.Repository
	defaultTaskMaxRetry int
}

func NewRepository(
	db *sql.DB,
	txm *mysql.TxManager,
	taskRepo *task.Repository,
	outboxRepo *outbox.Repository,
	defaultTaskMaxRetry int,
) *Repository {
	if defaultTaskMaxRetry <= 0 {
		defaultTaskMaxRetry = 5
	}
	if txm == nil {
		created, _ := mysql.NewTxManager(db)
		txm = created
	}
	return &Repository{
		db:                  db,
		txm:                 txm,
		taskRepo:            taskRepo,
		outboxRepo:          outboxRepo,
		defaultTaskMaxRetry: defaultTaskMaxRetry,
	}
}

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

		insertRes, err := tx.ExecContext(ctx, `
INSERT INTO coupon_claims
	(coupon_id, user_id, status, idempotency_key, created_at, updated_at)
VALUES
	(?, ?, 'CLAIMED', NULLIF(?, ''), ?, ?)
`, couponID, userID, idemKey, now, now)
		if err != nil {
			if isDuplicateKey(err) {
				if idemKey != "" {
					existing, found, findErr := r.findClaimByIdemTx(ctx, tx, couponID, userID, idemKey)
					if findErr != nil {
						return findErr
					}
					if found {
						rec = existing
						return nil
					}
				}
				return ErrAlreadyClaimed
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
		if enqueueErr := r.createTaskAndOutbox(ctx, tx, rec); enqueueErr != nil {
			return enqueueErr
		}
		return nil
	})
	if err != nil {
		return ClaimRecord{}, err
	}
	return rec, nil
}

func (r *Repository) FindClaimByID(ctx context.Context, claimID int64) (ClaimRecord, error) {
	var rec ClaimRecord
	var idem sql.NullString
	err := r.db.QueryRowContext(ctx, `
SELECT claim_id, coupon_id, user_id, status, idempotency_key, created_at
FROM coupon_claims
WHERE claim_id = ?
LIMIT 1
`, claimID).Scan(&rec.ID, &rec.CouponID, &rec.UserID, &rec.Status, &idem, &rec.CreatedAt)
	if err != nil {
		return ClaimRecord{}, err
	}
	if idem.Valid {
		rec.IdempotencyKey = idem.String
	}
	return rec, nil
}

func (r *Repository) FindClaimByUser(ctx context.Context, couponID, userID int64) (ClaimRecord, error) {
	var rec ClaimRecord
	var idem sql.NullString
	err := r.db.QueryRowContext(ctx, `
SELECT claim_id, coupon_id, user_id, status, idempotency_key, created_at
FROM coupon_claims
WHERE coupon_id = ? AND user_id = ?
ORDER BY claim_id DESC
LIMIT 1
`, couponID, userID).Scan(&rec.ID, &rec.CouponID, &rec.UserID, &rec.Status, &idem, &rec.CreatedAt)
	if err != nil {
		return ClaimRecord{}, err
	}
	if idem.Valid {
		rec.IdempotencyKey = idem.String
	}
	return rec, nil
}

type campaignSnapshot struct {
	Status         string
	AvailableStock int
	PerUserLimit   int
	StartAt        time.Time
	EndAt          time.Time
}

func (r *Repository) loadCampaignForUpdate(ctx context.Context, tx *sql.Tx, couponID int64) (campaignSnapshot, error) {
	var campaign campaignSnapshot
	err := tx.QueryRowContext(ctx, `
SELECT status, available_stock, per_user_limit, start_at, end_at
FROM coupon_campaigns
WHERE coupon_id = ?
FOR UPDATE
`, couponID).Scan(&campaign.Status, &campaign.AvailableStock, &campaign.PerUserLimit, &campaign.StartAt, &campaign.EndAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return campaignSnapshot{}, ErrCampaignNotFound
		}
		return campaignSnapshot{}, fmt.Errorf("query campaign for update: %w", err)
	}
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

func (r *Repository) findClaimByIdemTx(ctx context.Context, tx *sql.Tx, couponID, userID int64, idemKey string) (ClaimRecord, bool, error) {
	var (
		rec  ClaimRecord
		idem sql.NullString
	)
	err := tx.QueryRowContext(ctx, `
SELECT claim_id, coupon_id, user_id, status, idempotency_key, created_at
FROM coupon_claims
WHERE coupon_id = ? AND user_id = ? AND idempotency_key = ?
LIMIT 1
`, couponID, userID, idemKey).Scan(&rec.ID, &rec.CouponID, &rec.UserID, &rec.Status, &idem, &rec.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ClaimRecord{}, false, nil
		}
		return ClaimRecord{}, false, fmt.Errorf("query claim by idempotency key for update: %w", err)
	}
	if idem.Valid {
		rec.IdempotencyKey = idem.String
	}
	return rec, true, nil
}

func (r *Repository) createTaskAndOutbox(ctx context.Context, tx *sql.Tx, rec ClaimRecord) error {
	if r.taskRepo == nil || r.outboxRepo == nil {
		return nil
	}
	payload, err := task.MarshalPayload(task.SendCouponNoticePayload{
		ClaimID:  rec.ID,
		CouponID: rec.CouponID,
		UserID:   rec.UserID,
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

func isDuplicateKey(err error) bool {
	var me *mysqlerr.MySQLError
	if errors.As(err, &me) {
		return me.Number == 1062
	}
	return false
}
