package task

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"mini-jupiter/examples/Quan/internal/outbox"
	apperr "mini-jupiter/pkg/errors"
	"mini-jupiter/pkg/mysql"
)

type Service struct {
	txm          *mysql.TxManager
	repo         *Repository
	outboxRepo   *outbox.Repository
	queue        dlqQueue
	defaultRetry int
}

type dlqQueue interface {
	ReplayFromDLQ(ctx context.Context, taskID int64) (bool, error)
}

func NewService(txm *mysql.TxManager, repo *Repository, outboxRepo *outbox.Repository, defaultRetry int) *Service {
	return NewServiceWithQueue(txm, repo, outboxRepo, nil, defaultRetry)
}

func NewServiceWithQueue(txm *mysql.TxManager, repo *Repository, outboxRepo *outbox.Repository, queue dlqQueue, defaultRetry int) *Service {
	if defaultRetry <= 0 {
		defaultRetry = 5
	}
	return &Service{
		txm:          txm,
		repo:         repo,
		outboxRepo:   outboxRepo,
		queue:        queue,
		defaultRetry: defaultRetry,
	}
}

type CreateTaskRequest struct {
	TaskType string
	BizID    string
	Payload  []byte
	MaxRetry int
}

func (s *Service) CreateTask(ctx context.Context, req CreateTaskRequest) (AsyncTask, error) {
	req.TaskType = strings.TrimSpace(req.TaskType)
	req.BizID = strings.TrimSpace(req.BizID)
	if req.TaskType == "" || req.BizID == "" || len(req.Payload) == 0 {
		return AsyncTask{}, apperr.New(apperr.CodeBadRequest, "invalid task request")
	}
	if req.MaxRetry <= 0 {
		req.MaxRetry = s.defaultRetry
	}
	var created AsyncTask
	err := s.txm.WithinTx(ctx, nil, func(ctx context.Context, tx *sql.Tx) error {
		task, err := s.repo.CreateTx(ctx, tx, CreateTaskParams{
			TaskType: req.TaskType,
			BizID:    req.BizID,
			Payload:  req.Payload,
			MaxRetry: req.MaxRetry,
		})
		if err != nil {
			if errors.Is(err, ErrTaskDuplicate) {
				existing, getErr := s.repo.GetByTypeBiz(ctx, req.TaskType, req.BizID)
				if getErr != nil {
					return fmt.Errorf("query duplicated task: %w", getErr)
				}
				created = existing
				return nil
			}
			return err
		}
		created = task
		payload, err := outbox.MarshalTaskCreatedPayload(task.ID)
		if err != nil {
			return fmt.Errorf("marshal outbox payload: %w", err)
		}
		_, err = s.outboxRepo.CreateTx(ctx, tx, outbox.CreateEventParams{
			EventType:     outbox.EventTypeTaskCreated,
			AggregateType: "async_task",
			AggregateID:   strconv.FormatInt(task.ID, 10),
			PayloadJSON:   payload,
		})
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return AsyncTask{}, apperr.Wrap(apperr.CodeInternalError, "create task failed", err)
	}
	return created, nil
}

func (s *Service) GetTask(ctx context.Context, taskID int64) (AsyncTask, error) {
	task, err := s.repo.GetByID(ctx, taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrTaskNotFound) {
			return AsyncTask{}, apperr.New(apperr.CodeNotFound, "task not found")
		}
		return AsyncTask{}, apperr.Wrap(apperr.CodeInternalError, "query task failed", err)
	}
	return task, nil
}

func (s *Service) ReplayDLQTask(ctx context.Context, taskID int64) error {
	if taskID <= 0 {
		return apperr.New(apperr.CodeBadRequest, "invalid task_id")
	}
	if s.queue == nil {
		return apperr.New(apperr.CodeInternalError, "dlq replay queue not configured")
	}

	if err := s.repo.MarkReplayReady(ctx, taskID); err != nil {
		switch {
		case errors.Is(err, ErrTaskNotFound):
			return apperr.New(apperr.CodeNotFound, "task not found")
		case errors.Is(err, ErrTaskNotReplayable):
			return apperr.New(apperr.CodeConflict, "task is not replayable")
		default:
			return apperr.Wrap(apperr.CodeInternalError, "mark replay task status failed", err)
		}
	}

	moved, err := s.queue.ReplayFromDLQ(ctx, taskID)
	if err != nil {
		_ = s.repo.RestoreDeadAfterReplayFailure(ctx, taskID)
		return apperr.Wrap(apperr.CodeInternalError, "replay task from dlq failed", err)
	}
	if !moved {
		_ = s.repo.RestoreDeadAfterReplayFailure(ctx, taskID)
		return apperr.New(apperr.CodeNotFound, "task not found in dlq")
	}
	return nil
}
