package outbox

import (
	"database/sql"

	outboxmodel "mini-jupiter/examples/Quan/internal/outbox/model"
	outboxrepo "mini-jupiter/examples/Quan/internal/outbox/repository"
)

type Event = outboxmodel.Event
type CreateEventParams = outboxmodel.CreateEventParams
type TaskCreatedPayload = outboxmodel.TaskCreatedPayload
type Repository = outboxrepo.Repository

const (
	StatusPending     = outboxmodel.StatusPending
	StatusDispatching = outboxmodel.StatusDispatching
	StatusPublished   = outboxmodel.StatusPublished
	StatusSuspended   = outboxmodel.StatusSuspended

	EventTypeTaskCreated = outboxmodel.EventTypeTaskCreated
)

func NewRepository(db *sql.DB) *Repository {
	return outboxrepo.NewRepository(db)
}

func MarshalTaskCreatedPayload(taskID int64) ([]byte, error) {
	return outboxmodel.MarshalTaskCreatedPayload(taskID)
}

func ParseTaskCreatedPayload(payload []byte) (TaskCreatedPayload, error) {
	return outboxmodel.ParseTaskCreatedPayload(payload)
}
