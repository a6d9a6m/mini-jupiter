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
	defaultRetry int
}

func NewService(txm *mysql.TxManager, repo *Repository, outboxRepo *outbox.Repository, defaultRetry int) *Service {
	if defaultRetry <= 0 {
		defaultRetry = 5
	}
	return &Service{
		txm:          txm,
		repo:         repo,
		outboxRepo:   outboxRepo,
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
