package sideeffect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	mysqlerr "github.com/go-sql-driver/mysql"
)

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	if db == nil {
		return nil
	}
	return &Repository{db: db}
}

func (r *Repository) StageClaimCreatedTx(ctx context.Context, tx *sql.Tx, claimID, couponID, userID int64, traceID string) error {
	if r == nil {
		return nil
	}
	payload, err := MarshalPayload(Payload{
		ClaimID:  claimID,
		CouponID: couponID,
		UserID:   userID,
		TraceID:  traceID,
	})
	if err != nil {
		return fmt.Errorf("marshal claim side effect payload: %w", err)
	}
	_, err = r.CreateTx(ctx, tx, CreateParams{
		ClaimID:     claimID,
		EffectType:  TypeClaimCreated,
		PayloadJSON: payload,
	})
	if err != nil {
		if errors.Is(err, ErrDuplicate) {
			return nil
		}
		return fmt.Errorf("create claim side effect in claim tx: %w", err)
	}
	return nil
}

func (r *Repository) CreateTx(ctx context.Context, tx *sql.Tx, p CreateParams) (Record, error) {
	if r == nil {
		return Record{}, nil
	}
	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
INSERT INTO claim_side_effects
	(claim_id, effect_type, payload_json, status, retry_count, next_retry_at, last_error, async_task_id, outbox_event_id, created_at, updated_at)
VALUES
	(?, ?, ?, ?, 0, ?, '', NULL, NULL, ?, ?)
`, p.ClaimID, p.EffectType, p.PayloadJSON, StatusPending, now, now, now)
	if err != nil {
		if isDuplicateKey(err) {
			return Record{}, ErrDuplicate
		}
		return Record{}, fmt.Errorf("insert claim side effect: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Record{}, fmt.Errorf("claim side effect last insert id: %w", err)
	}
	return Record{
		ID:          id,
		ClaimID:     p.ClaimID,
		EffectType:  p.EffectType,
		PayloadJSON: p.PayloadJSON,
		Status:      StatusPending,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

func (r *Repository) Create(ctx context.Context, p CreateParams) (Record, error) {
	if r == nil {
		return Record{}, nil
	}
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
INSERT INTO claim_side_effects
	(claim_id, effect_type, payload_json, status, retry_count, next_retry_at, last_error, async_task_id, outbox_event_id, created_at, updated_at)
VALUES
	(?, ?, ?, ?, 0, ?, '', NULL, NULL, ?, ?)
`, p.ClaimID, p.EffectType, p.PayloadJSON, StatusPending, now, now, now)
	if err != nil {
		if isDuplicateKey(err) {
			return Record{}, ErrDuplicate
		}
		return Record{}, fmt.Errorf("insert claim side effect: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Record{}, fmt.Errorf("claim side effect last insert id: %w", err)
	}
	return Record{
		ID:          id,
		ClaimID:     p.ClaimID,
		EffectType:  p.EffectType,
		PayloadJSON: p.PayloadJSON,
		Status:      StatusPending,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

func (r *Repository) GetByClaimEffect(ctx context.Context, claimID int64, effectType string) (Record, error) {
	return ScanRecord(r.db.QueryRowContext(ctx, `
SELECT side_effect_id, claim_id, effect_type, payload_json, status, retry_count, last_error,
       COALESCE(async_task_id, 0), COALESCE(outbox_event_id, 0), created_at, updated_at
FROM claim_side_effects
WHERE claim_id = ? AND effect_type = ?
LIMIT 1
`, claimID, effectType))
}

func (r *Repository) ListDispatchable(ctx context.Context, limit int) ([]Record, error) {
	if limit <= 0 {
		limit = 100
	}
	now := time.Now().UTC()
	rows, err := r.db.QueryContext(ctx, `
SELECT side_effect_id, claim_id, effect_type, payload_json, status, retry_count, last_error,
       COALESCE(async_task_id, 0), COALESCE(outbox_event_id, 0), created_at, updated_at
FROM claim_side_effects
WHERE status = ? AND next_retry_at <= ?
ORDER BY side_effect_id ASC
LIMIT ?
`, StatusPending, now, limit)
	if err != nil {
		return nil, fmt.Errorf("query claim side effects: %w", err)
	}
	defer rows.Close()

	out := make([]Record, 0, limit)
	for rows.Next() {
		rec, scanErr := ScanRecord(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate claim side effects: %w", err)
	}
	return out, nil
}

func (r *Repository) TryMarkProcessing(ctx context.Context, sideEffectID int64) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
UPDATE claim_side_effects
SET status = ?, last_error = '', updated_at = ?
WHERE side_effect_id = ? AND status = ?
`, StatusProcessing, time.Now().UTC(), sideEffectID, StatusPending)
	if err != nil {
		return false, fmt.Errorf("mark claim side effect processing: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("mark claim side effect processing rows affected: %w", err)
	}
	return affected > 0, nil
}

func (r *Repository) MarkDone(ctx context.Context, sideEffectID, asyncTaskID, outboxEventID int64) error {
	_, err := r.db.ExecContext(ctx, `
UPDATE claim_side_effects
SET status = ?, async_task_id = ?, outbox_event_id = ?, last_error = '', updated_at = ?
WHERE side_effect_id = ? AND status IN (?, ?)
`, StatusDone, asyncTaskID, outboxEventID, time.Now().UTC(), sideEffectID, StatusPending, StatusProcessing)
	if err != nil {
		return fmt.Errorf("mark claim side effect done: %w", err)
	}
	return nil
}

func (r *Repository) MarkRetry(ctx context.Context, sideEffectID int64, delay time.Duration, lastErr string) error {
	next := time.Now().UTC().Add(delay)
	_, err := r.db.ExecContext(ctx, `
UPDATE claim_side_effects
SET status = ?, retry_count = retry_count + 1, next_retry_at = ?, last_error = ?, updated_at = ?
WHERE side_effect_id = ? AND status = ?
`, StatusPending, next, truncateError(lastErr), time.Now().UTC(), sideEffectID, StatusProcessing)
	if err != nil {
		return fmt.Errorf("mark claim side effect retry: %w", err)
	}
	return nil
}

func (r *Repository) MarkSuspended(ctx context.Context, sideEffectID int64, lastErr string) error {
	_, err := r.db.ExecContext(ctx, `
UPDATE claim_side_effects
SET status = ?, retry_count = retry_count + 1, last_error = ?, updated_at = ?
WHERE side_effect_id = ? AND status = ?
`, StatusSuspended, truncateError(lastErr), time.Now().UTC(), sideEffectID, StatusProcessing)
	if err != nil {
		return fmt.Errorf("mark claim side effect suspended: %w", err)
	}
	return nil
}

func (r *Repository) RecoverStaleProcessing(ctx context.Context, staleBefore time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = 100
	}
	res, err := r.db.ExecContext(ctx, `
UPDATE claim_side_effects
SET status = ?, last_error = ?, updated_at = ?
WHERE side_effect_id IN (
	SELECT side_effect_id
	FROM (
		SELECT side_effect_id
		FROM claim_side_effects
		WHERE status = ? AND updated_at <= ?
		ORDER BY updated_at ASC
		LIMIT ?
	) AS stale_side_effects
)
`, StatusPending, "side effect processing timeout recovered for retry", time.Now().UTC(), StatusProcessing, staleBefore, limit)
	if err != nil {
		return 0, fmt.Errorf("recover stale claim side effects: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("recover stale claim side effects rows affected: %w", err)
	}
	return affected, nil
}

func ScanRecord(row interface{ Scan(dest ...any) error }) (Record, error) {
	var rec Record
	if err := row.Scan(
		&rec.ID,
		&rec.ClaimID,
		&rec.EffectType,
		&rec.PayloadJSON,
		&rec.Status,
		&rec.RetryCount,
		&rec.LastError,
		&rec.AsyncTaskID,
		&rec.OutboxEventID,
		&rec.CreatedAt,
		&rec.UpdatedAt,
	); err != nil {
		return Record{}, fmt.Errorf("scan claim side effect: %w", err)
	}
	return rec, nil
}

func truncateError(s string) string {
	if len(s) <= 255 {
		return s
	}
	return s[:255]
}

func isDuplicateKey(err error) bool {
	var me *mysqlerr.MySQLError
	if errors.As(err, &me) {
		return me.Number == 1062
	}
	return false
}
