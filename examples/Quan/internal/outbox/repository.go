package outbox

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) CreateTx(ctx context.Context, tx *sql.Tx, p CreateEventParams) (Event, error) {
	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
INSERT INTO outbox_events
	(event_type, aggregate_type, aggregate_id, payload_json, status, retry_count, next_retry_at, last_error, created_at, updated_at)
VALUES
	(?, ?, ?, ?, ?, 0, ?, '', ?, ?)
`, p.EventType, p.AggregateType, p.AggregateID, p.PayloadJSON, StatusPending, now, now, now)
	if err != nil {
		return Event{}, fmt.Errorf("insert outbox event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Event{}, fmt.Errorf("outbox event last insert id: %w", err)
	}
	return Event{
		ID:            id,
		EventType:     p.EventType,
		AggregateType: p.AggregateType,
		AggregateID:   p.AggregateID,
		PayloadJSON:   p.PayloadJSON,
		Status:        StatusPending,
	}, nil
}

func (r *Repository) ListDispatchable(ctx context.Context, limit int) ([]Event, error) {
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
`, StatusPending, now, limit)
	if err != nil {
		return nil, fmt.Errorf("query dispatchable outbox events: %w", err)
	}
	defer rows.Close()

	events := make([]Event, 0, limit)
	for rows.Next() {
		var evt Event
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
WHERE status = ?
`, StatusPending).Scan(&cnt); err != nil {
		return 0, fmt.Errorf("count pending outbox events: %w", err)
	}
	return cnt, nil
}

func (r *Repository) MarkPublished(ctx context.Context, eventID int64) error {
	_, err := r.db.ExecContext(ctx, `
UPDATE outbox_events
SET status = ?, last_error = '', updated_at = ?
WHERE event_id = ? AND status = ?
`, StatusPublished, time.Now().UTC(), eventID, StatusPending)
	if err != nil {
		return fmt.Errorf("mark outbox event published: %w", err)
	}
	return nil
}

func (r *Repository) MarkRetry(ctx context.Context, eventID int64, delay time.Duration, lastErr string) error {
	next := time.Now().UTC().Add(delay)
	_, err := r.db.ExecContext(ctx, `
UPDATE outbox_events
SET retry_count = retry_count + 1, next_retry_at = ?, last_error = ?, updated_at = ?
WHERE event_id = ? AND status = ?
`, next, truncate(lastErr, 255), time.Now().UTC(), eventID, StatusPending)
	if err != nil {
		return fmt.Errorf("mark outbox event retry: %w", err)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
