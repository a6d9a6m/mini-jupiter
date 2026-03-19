package taskmodel

import (
	"encoding/json"
	"time"
)

const (
	StatusPending   = "PENDING"
	StatusRunning   = "RUNNING"
	StatusSuccess   = "SUCCESS"
	StatusFailed    = "FAILED"
	StatusSuspended = "SUSPENDED"
	StatusDead      = "DEAD"
)

const (
	TaskTypeSendCouponNotice = "SEND_COUPON_NOTICE"
)

type AsyncTask struct {
	ID         int64
	TaskType   string
	BizID      string
	Status     string
	Payload    []byte
	RetryCount int
	MaxRetry   int
	NextRetry  *time.Time
	LastError  string
	Version    int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type CreateTaskParams struct {
	TaskType string
	BizID    string
	Payload  []byte
	MaxRetry int
}

const (
	RecoverySourceRetryDue     = "retry_due"
	RecoverySourceSuspended    = "suspended"
	RecoverySourceStaleRunning = "stale_running"
)

type RecoveryCandidate struct {
	TaskID    int64
	Source    string
	RecoverAt time.Time
	Version   int64
}

type SendCouponNoticePayload struct {
	ClaimID  int64  `json:"claim_id"`
	CouponID int64  `json:"coupon_id"`
	UserID   int64  `json:"user_id"`
	TraceID  string `json:"trace_id,omitempty"`
}

func MarshalPayload(v any) ([]byte, error) {
	return json.Marshal(v)
}
