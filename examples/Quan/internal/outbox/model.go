package outbox

import "encoding/json"

const (
	StatusPending     = "PENDING"
	StatusDispatching = "DISPATCHING"
	StatusPublished   = "PUBLISHED"
	StatusSuspended   = "SUSPENDED"

	EventTypeTaskCreated = "TASK_CREATED"
)

type Event struct {
	ID            int64
	EventType     string
	AggregateType string
	AggregateID   string
	PayloadJSON   []byte
	Status        string
	RetryCount    int
	LastError     string
}

type CreateEventParams struct {
	EventType     string
	AggregateType string
	AggregateID   string
	PayloadJSON   []byte
}

type TaskCreatedPayload struct {
	TaskID int64 `json:"task_id"`
}

func MarshalTaskCreatedPayload(taskID int64) ([]byte, error) {
	return json.Marshal(TaskCreatedPayload{TaskID: taskID})
}

func ParseTaskCreatedPayload(payload []byte) (TaskCreatedPayload, error) {
	var p TaskCreatedPayload
	err := json.Unmarshal(payload, &p)
	return p, err
}
