package outboxrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	outboxmodel "mini-jupiter/examples/Quan/internal/outbox/model"
)

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Create(ctx context.Context, p outboxmodel.CreateEventParams) (outboxmodel.Event, error) {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
INSERT INTO outbox_events
	(event_type, aggregate_type, aggregate_id, payload_json, status, retry_count, next_retry_at, last_error, created_at, updated_at)
VALUES
	(?, ?, ?, ?, ?, 0, ?, '', ?, ?)
`, p.EventType, p.AggregateType, p.AggregateID, p.PayloadJSON, outboxmodel.StatusPending, now, now, now)
	if err != nil {
		return outboxmodel.Event{}, fmt.Errorf("insert outbox event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return outboxmodel.Event{}, fmt.Errorf("outbox event last insert id: %w", err)
	}
	return outboxmodel.Event{
		ID:            id,
		EventType:     p.EventType,
		AggregateType: p.AggregateType,
		AggregateID:   p.AggregateID,
		PayloadJSON:   p.PayloadJSON,
		Status:        outboxmodel.StatusPending,
	}, nil
}

func (r *Repository) CreateTx(ctx context.Context, tx *sql.Tx, p outboxmodel.CreateEventParams) (outboxmodel.Event, error) {
	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
INSERT INTO outbox_events
	(event_type, aggregate_type, aggregate_id, payload_json, status, retry_count, next_retry_at, last_error, created_at, updated_at)
VALUES
	(?, ?, ?, ?, ?, 0, ?, '', ?, ?)
`, p.EventType, p.AggregateType, p.AggregateID, p.PayloadJSON, outboxmodel.StatusPending, now, now, now)
	if err != nil {
		return outboxmodel.Event{}, fmt.Errorf("insert outbox event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return outboxmodel.Event{}, fmt.Errorf("outbox event last insert id: %w", err)
	}
	return outboxmodel.Event{
		ID:            id,
		EventType:     p.EventType,
		AggregateType: p.AggregateType,
		AggregateID:   p.AggregateID,
		PayloadJSON:   p.PayloadJSON,
		Status:        outboxmodel.StatusPending,
	}, nil
}

func (r *Repository) FindByAggregate(ctx context.Context, eventType, aggregateType, aggregateID string) (outboxmodel.Event, bool, error) {
	var evt outboxmodel.Event
	err := r.db.QueryRowContext(ctx, `
SELECT event_id, event_type, aggregate_type, aggregate_id, payload_json, status, retry_count, last_error
FROM outbox_events
WHERE event_type = ? AND aggregate_type = ? AND aggregate_id = ?
ORDER BY event_id DESC
LIMIT 1
`, eventType, aggregateType, aggregateID).Scan(
		&evt.ID,
		&evt.EventType,
		&evt.AggregateType,
		&evt.AggregateID,
		&evt.PayloadJSON,
		&evt.Status,
		&evt.RetryCount,
		&evt.LastError,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return outboxmodel.Event{}, false, nil
		}
		return outboxmodel.Event{}, false, fmt.Errorf("query outbox event by aggregate: %w", err)
	}
	return evt, true, nil
}

func (r *Repository) ListDispatchable(ctx context.Context, limit int) ([]outboxmodel.Event, error) {
	if limit <= 0 {
		limit = 100
	}
	now := time.Now().UTC()
	rows, err := r.db.QueryContext(ctx, `
SELECT event_id, event_type, aggregate_type, aggregate_id, payload_json, status, retry_count, last_error
FROM outbox_events
WHERE status = ? AND next_retry_at <= ?
ORDER BY event_id ASC
LIMIT ?
`, outboxmodel.StatusPending, now, limit)
	if err != nil {
		return nil, fmt.Errorf("query dispatchable outbox events: %w", err)
	}
	defer rows.Close()

	events := make([]outboxmodel.Event, 0, limit)
	for rows.Next() {
		var evt outboxmodel.Event
		if scanErr := rows.Scan(
			&evt.ID,
			&evt.EventType,
			&evt.AggregateType,
			&evt.AggregateID,
			&evt.PayloadJSON,
			&evt.Status,
			&evt.RetryCount,
			&evt.LastError,
		); scanErr != nil {
			return nil, fmt.Errorf("scan outbox event: %w", scanErr)
		}
		events = append(events, evt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate outbox events: %w", err)
	}
	return events, nil
}

func (r *Repository) CountPending(ctx context.Context) (int64, error) {
	var cnt int64
	if err := r.db.QueryRowContext(ctx, `
SELECT COUNT(1)
FROM outbox_events
WHERE status IN (?, ?, ?)
`, outboxmodel.StatusPending, outboxmodel.StatusDispatching, outboxmodel.StatusSuspended).Scan(&cnt); err != nil {
		return 0, fmt.Errorf("count pending outbox events: %w", err)
	}
	return cnt, nil
}

func (r *Repository) TryMarkDispatching(ctx context.Context, eventID int64) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
UPDATE outbox_events
SET status = ?, last_error = '', updated_at = ?
WHERE event_id = ? AND status = ?
`, outboxmodel.StatusDispatching, time.Now().UTC(), eventID, outboxmodel.StatusPending)
	if err != nil {
		return false, fmt.Errorf("mark outbox event dispatching: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("mark outbox event dispatching rows affected: %w", err)
	}
	return affected > 0, nil
}

func (r *Repository) MarkPublished(ctx context.Context, eventID int64) error {
	_, err := r.db.ExecContext(ctx, `
UPDATE outbox_events
SET status = ?, last_error = '', updated_at = ?
WHERE event_id = ? AND status = ?
`, outboxmodel.StatusPublished, time.Now().UTC(), eventID, outboxmodel.StatusDispatching)
	if err != nil {
		return fmt.Errorf("mark outbox event published: %w", err)
	}
	return nil
}

func (r *Repository) MarkRetry(ctx context.Context, eventID int64, delay time.Duration, lastErr string) error {
	next := time.Now().UTC().Add(delay)
	_, err := r.db.ExecContext(ctx, `
UPDATE outbox_events
SET status = ?, retry_count = retry_count + 1, next_retry_at = ?, last_error = ?, updated_at = ?
WHERE event_id = ? AND status = ?
`, outboxmodel.StatusPending, next, truncate(lastErr, 255), time.Now().UTC(), eventID, outboxmodel.StatusDispatching)
	if err != nil {
		return fmt.Errorf("mark outbox event retry: %w", err)
	}
	return nil
}

func (r *Repository) MarkSuspended(ctx context.Context, eventID int64, lastErr string) error {
	_, err := r.db.ExecContext(ctx, `
UPDATE outbox_events
SET status = ?, last_error = ?, updated_at = ?
WHERE event_id = ? AND status IN (?, ?)
`, outboxmodel.StatusSuspended, truncate(lastErr, 255), time.Now().UTC(), eventID, outboxmodel.StatusPending, outboxmodel.StatusDispatching)
	if err != nil {
		return fmt.Errorf("mark outbox event suspended: %w", err)
	}
	return nil
}

func (r *Repository) RecoverStaleDispatching(ctx context.Context, staleBefore time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = 100
	}
	res, err := r.db.ExecContext(ctx, `
UPDATE outbox_events
SET status = ?, last_error = ?, updated_at = ?
WHERE event_id IN (
	SELECT event_id
	FROM (
		SELECT event_id
		FROM outbox_events
		WHERE status = ? AND updated_at <= ?
		ORDER BY updated_at ASC
		LIMIT ?
	) AS stale_events
)
`, outboxmodel.StatusPending, "dispatch timeout recovered for retry", time.Now().UTC(), outboxmodel.StatusDispatching, staleBefore, limit)
	if err != nil {
		return 0, fmt.Errorf("recover stale dispatching outbox events: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("recover stale dispatching outbox rows affected: %w", err)
	}
	return affected, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
