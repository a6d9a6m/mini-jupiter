package task

import (
	"context"
	"database/sql"
	"time"

	"mini-jupiter/examples/Quan/internal/outbox"
	compensator "mini-jupiter/examples/Quan/internal/recovery/compensator"
	taskhandler "mini-jupiter/examples/Quan/internal/task/handler"
	taskmodel "mini-jupiter/examples/Quan/internal/task/model"
	taskrepo "mini-jupiter/examples/Quan/internal/task/repository"
	taskservice "mini-jupiter/examples/Quan/internal/task/service"
	"mini-jupiter/pkg/mysql"
)

type AsyncTask = taskmodel.AsyncTask
type CreateTaskParams = taskmodel.CreateTaskParams
type RecoveryCandidate = taskmodel.RecoveryCandidate
type SendCouponNoticePayload = taskmodel.SendCouponNoticePayload

type Repository = taskrepo.Repository

type TaskHandler = taskhandler.TaskHandler
type HandlerRegistry = taskhandler.Registry
type SendCouponNoticeHandler = taskhandler.SendCouponNoticeHandler
type ConsumeReceiptRepository = taskhandler.ConsumeReceiptRepository

type Service = taskservice.Service
type CreateTaskRequest = taskservice.CreateTaskRequest

type CompensationConfig = compensator.Config

type Compensator struct {
	*compensator.Compensator
}

const (
	StatusPending   = taskmodel.StatusPending
	StatusRunning   = taskmodel.StatusRunning
	StatusSuccess   = taskmodel.StatusSuccess
	StatusFailed    = taskmodel.StatusFailed
	StatusSuspended = taskmodel.StatusSuspended
	StatusDead      = taskmodel.StatusDead

	TaskTypeSendCouponNotice = taskmodel.TaskTypeSendCouponNotice

	RecoverySourceRetryDue     = taskmodel.RecoverySourceRetryDue
	RecoverySourceSuspended    = taskmodel.RecoverySourceSuspended
	RecoverySourceStaleRunning = taskmodel.RecoverySourceStaleRunning
)

var (
	ErrTaskDuplicate       = taskrepo.ErrTaskDuplicate
	ErrTaskNotFound        = taskrepo.ErrTaskNotFound
	ErrTaskNotReplayable   = taskrepo.ErrTaskNotReplayable
	ErrTaskVersionConflict = taskrepo.ErrTaskVersionConflict
)

type dlqQueue interface {
	ReplayFromDLQ(ctx context.Context, taskID int64) (bool, error)
}

func MarshalPayload(v any) ([]byte, error) {
	return taskmodel.MarshalPayload(v)
}

func NewRepository(db *sql.DB, txm *mysql.TxManager) *Repository {
	return taskrepo.NewRepository(db, txm)
}

func NewHandlerRegistry() *HandlerRegistry {
	return taskhandler.NewRegistry()
}

func NewConsumeReceiptRepository(db *sql.DB) *ConsumeReceiptRepository {
	return taskhandler.NewConsumeReceiptRepository(db)
}

func NewSendCouponNoticeHandler(receipts *ConsumeReceiptRepository) *SendCouponNoticeHandler {
	return taskhandler.NewSendCouponNoticeHandler(receipts)
}

func NewService(txm *mysql.TxManager, repo *Repository, outboxRepo *outbox.Repository, defaultRetry int) *Service {
	return taskservice.NewService(txm, repo, outboxRepo, defaultRetry)
}

func NewServiceWithQueue(txm *mysql.TxManager, repo *Repository, outboxRepo *outbox.Repository, queue dlqQueue, defaultRetry int) *Service {
	return taskservice.NewServiceWithQueue(txm, repo, outboxRepo, queue, defaultRetry)
}

func NewCompensator(repo interface {
	ListDueFailedForCompensation(ctx context.Context, limit int) ([]RecoveryCandidate, error)
	ListSuspendedForCompensation(ctx context.Context, staleBefore time.Time, limit int) ([]RecoveryCandidate, error)
	ListStaleRunningForCompensation(ctx context.Context, staleBefore time.Time, limit int) ([]RecoveryCandidate, error)
	MarkRecoveredForRetry(ctx context.Context, taskID int64, expectedVersion int64, lastErr string) (bool, error)
}, queue interface {
	ScheduleRetry(ctx context.Context, taskID int64, retryAt time.Time) error
}, cfg CompensationConfig) (*Compensator, error) {
	comp, err := compensator.New(repo, queue, cfg)
	if err != nil {
		return nil, err
	}
	return &Compensator{Compensator: comp}, nil
}

func (c *Compensator) compensateOnce(ctx context.Context) error {
	if c == nil || c.Compensator == nil {
		return nil
	}
	return c.Compensator.CompensateOnce(ctx)
}
