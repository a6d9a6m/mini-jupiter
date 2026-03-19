package sideeffect

import (
	"encoding/json"
	"errors"
	"time"
)

const (
	TypeClaimCreated = "CLAIM_CREATED"

	StatusPending    = "PENDING"
	StatusProcessing = "PROCESSING"
	StatusDone       = "DONE"
	StatusSuspended  = "SUSPENDED"
)

var ErrDuplicate = errors.New("claim side effect duplicate")

type Record struct {
	ID            int64
	ClaimID       int64
	EffectType    string
	PayloadJSON   []byte
	Status        string
	RetryCount    int
	LastError     string
	AsyncTaskID   int64
	OutboxEventID int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type Payload struct {
	ClaimID  int64  `json:"claim_id"`
	CouponID int64  `json:"coupon_id"`
	UserID   int64  `json:"user_id"`
	TraceID  string `json:"trace_id,omitempty"`
}

type CreateParams struct {
	ClaimID     int64
	EffectType  string
	PayloadJSON []byte
}

func MarshalPayload(payload Payload) ([]byte, error) {
	return json.Marshal(payload)
}

func ParsePayload(payload []byte) (Payload, error) {
	var out Payload
	err := json.Unmarshal(payload, &out)
	return out, err
}
