package request

import (
	"context"
	"errors"
	"time"
)

var ErrNotImplemented = errors.New("not implemented")
var ErrRequestExists = errors.New("request already exists")
var ErrRequestNotFound = errors.New("request not found")

type RetryableError struct {
	Err error
}

type TransitionSkippedError struct {
	RequestID string
	Current   Status
	Target    Status
}

func (e TransitionSkippedError) Error() string {
	return "request status transition skipped"
}

type DurabilityPendingError struct {
	RequestID string
	Status    Status
	Err       error
}

func (e DurabilityPendingError) Error() string {
	if e.Err == nil {
		return "replica confirmation pending"
	}
	return e.Err.Error()
}

func (e DurabilityPendingError) Unwrap() error {
	return e.Err
}

func IsDurabilityPendingError(err error) bool {
	var target DurabilityPendingError
	return errors.As(err, &target)
}

func AsDurabilityPendingError(err error) (DurabilityPendingError, bool) {
	var target DurabilityPendingError
	if !errors.As(err, &target) {
		return DurabilityPendingError{}, false
	}
	return target, true
}

func IsTransitionSkippedError(err error) bool {
	var target TransitionSkippedError
	return errors.As(err, &target)
}

func AsTransitionSkippedError(err error) (TransitionSkippedError, bool) {
	var target TransitionSkippedError
	if !errors.As(err, &target) {
		return TransitionSkippedError{}, false
	}
	return target, true
}

func (e RetryableError) Error() string {
	if e.Err == nil {
		return "retryable error"
	}
	return e.Err.Error()
}

func (e RetryableError) Unwrap() error {
	return e.Err
}

func IsRetryableError(err error) bool {
	var target RetryableError
	return errors.As(err, &target)
}

type Status string

const (
	StatusAccepted   Status = "ACCEPTED"
	StatusPublishing Status = "PUBLISHING"
	StatusEnqueued   Status = "ENQUEUED"
	StatusProcessing Status = "PROCESSING"
	StatusSucceeded  Status = "SUCCEEDED"
	StatusRolledBack Status = "ROLLED_BACK"
	StatusFailed     Status = "FAILED"
)

type ResultState string

const (
	ResultStateProcessing ResultState = "PROCESSING"
	ResultStateSucceeded  ResultState = "SUCCEEDED"
	ResultStateFailed     ResultState = "FAILED"
)

type DecisionCode string

const (
	DecisionCodeAdmitted DecisionCode = "ADMITTED"
	DecisionCodeIdemHit  DecisionCode = "IDEM_HIT"
	DecisionCodeRejected DecisionCode = "REJECTED"
)

type AcceptRequest struct {
	CouponID       int64
	UserID         int64
	IdempotencyKey string
}

type AcceptResponse struct {
	RequestID string
	Status    Status
	Warning   string
}

type Decision struct {
	Code      DecisionCode
	RequestID string
}

type Request struct {
	ID             string
	CouponID       int64
	UserID         int64
	IdempotencyKey string
	ReservationID  string
	Status         Status
	Version        int64
	ClaimID        int64
	FailureCode    string
	AcceptedAt     time.Time
	ProcessedAt    time.Time
	FinishedAt     time.Time
	UpdatedAt      time.Time
}

type QueryResult struct {
	RequestID   string
	State       ResultState
	Internal    Status
	ClaimID     int64
	FailureCode string
}

type HotPath interface {
	Decide(ctx context.Context, couponID, userID int64, idemKey string) (Decision, error)
	Finalize(ctx context.Context, couponID, userID int64, idemKey, requestID string, claimID int64) error
	Rollback(ctx context.Context, couponID, userID int64, idemKey, requestID string) error
}

type RequestStore interface {
	Create(ctx context.Context, req Request) error
	UpdateStatus(ctx context.Context, requestID string, status Status, claimID int64, failureCode string) error
	CompareAndUpdateStatus(ctx context.Context, snapshot Request, status Status, claimID int64, failureCode string) (bool, error)
	Get(ctx context.Context, requestID string) (Request, bool, error)
	FindByIdempotency(ctx context.Context, couponID, userID int64, idemKey string) (Request, bool, error)
	ListByStatuses(ctx context.Context, statuses []Status, limit int) ([]Request, error)
}

type Publisher interface {
	PublishAccepted(ctx context.Context, req Request) error
}

type ClaimWriter interface {
	PersistClaim(ctx context.Context, req Request) (claimID int64, inserted bool, err error)
}

type ClaimLookup interface {
	FindClaimID(ctx context.Context, req Request) (claimID int64, found bool, err error)
}

type ReconcilePolicy struct {
	PublishStaleAfter    time.Duration
	ProcessingStaleAfter time.Duration
}

func (p ReconcilePolicy) withDefaults() ReconcilePolicy {
	if p.PublishStaleAfter <= 0 {
		p.PublishStaleAfter = 5 * time.Second
	}
	if p.ProcessingStaleAfter <= 0 {
		p.ProcessingStaleAfter = 30 * time.Second
	}
	return p
}
