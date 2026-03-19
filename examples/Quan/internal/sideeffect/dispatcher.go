package sideeffect

import (
	"context"
	"fmt"
	"sync"
	"time"

	"mini-jupiter/examples/Quan/internal/outbox"
	"mini-jupiter/examples/Quan/internal/task"
	applog "mini-jupiter/pkg/log"

	"go.uber.org/zap"
)

type DispatchConfig struct {
	Enabled      bool          `mapstructure:"enabled" yaml:"enabled"`
	PollInterval time.Duration `mapstructure:"poll_interval" yaml:"poll_interval"`
	BatchSize    int           `mapstructure:"batch_size" yaml:"batch_size"`
	StaleTimeout time.Duration `mapstructure:"stale_timeout" yaml:"stale_timeout"`
	RetryDelay   time.Duration `mapstructure:"retry_delay" yaml:"retry_delay"`
	MaxRetry     int           `mapstructure:"max_retry" yaml:"max_retry"`
}

func (c DispatchConfig) withDefaults() DispatchConfig {
	if c.PollInterval <= 0 {
		c.PollInterval = 2 * time.Second
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 100
	}
	if c.StaleTimeout <= 0 {
		c.StaleTimeout = 30 * time.Second
	}
	if c.RetryDelay <= 0 {
		c.RetryDelay = 3 * time.Second
	}
	if c.MaxRetry <= 0 {
		c.MaxRetry = 10
	}
	return c
}

type Dispatcher struct {
	cfg      DispatchConfig
	repo     dispatchRepository
	taskRepo dispatchTaskRepository
	outbox   dispatchOutboxRepository

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type dispatchRepository interface {
	RecoverStaleProcessing(ctx context.Context, staleBefore time.Time, limit int) (int64, error)
	ListDispatchable(ctx context.Context, limit int) ([]Record, error)
	TryMarkProcessing(ctx context.Context, sideEffectID int64) (bool, error)
	MarkSuspended(ctx context.Context, sideEffectID int64, lastErr string) error
	MarkRetry(ctx context.Context, sideEffectID int64, delay time.Duration, lastErr string) error
	MarkDone(ctx context.Context, sideEffectID, asyncTaskID, outboxEventID int64) error
}

type dispatchTaskRepository interface {
	Create(ctx context.Context, p task.CreateTaskParams) (task.AsyncTask, error)
	GetByTypeBiz(ctx context.Context, taskType, bizID string) (task.AsyncTask, error)
}

type dispatchOutboxRepository interface {
	FindByAggregate(ctx context.Context, eventType, aggregateType, aggregateID string) (outbox.Event, bool, error)
	Create(ctx context.Context, p outbox.CreateEventParams) (outbox.Event, error)
}

func NewDispatcher(repo dispatchRepository, taskRepo dispatchTaskRepository, outboxRepo dispatchOutboxRepository, cfg DispatchConfig) (*Dispatcher, error) {
	cfg = cfg.withDefaults()
	if !cfg.Enabled {
		return &Dispatcher{cfg: cfg}, nil
	}
	if repo == nil {
		return nil, fmt.Errorf("coupon side effect repository is nil")
	}
	if taskRepo == nil {
		return nil, fmt.Errorf("coupon side effect dispatcher task repository is nil")
	}
	if outboxRepo == nil {
		return nil, fmt.Errorf("coupon side effect dispatcher outbox repository is nil")
	}
	return &Dispatcher{
		cfg:      cfg,
		repo:     repo,
		taskRepo: taskRepo,
		outbox:   outboxRepo,
	}, nil
}

func (d *Dispatcher) Start(ctx context.Context) error {
	if d == nil || !d.cfg.Enabled {
		return nil
	}
	loopCtx, cancel := context.WithCancel(ctx)
	d.cancel = cancel
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.run(loopCtx)
	}()
	return nil
}

func (d *Dispatcher) Stop(_ context.Context) error {
	if d == nil || !d.cfg.Enabled {
		return nil
	}
	if d.cancel != nil {
		d.cancel()
	}
	d.wg.Wait()
	return nil
}

func (d *Dispatcher) run(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.PollInterval)
	defer ticker.Stop()
	for {
		if err := d.RecoverAndDispatchOnce(ctx); err != nil && ctx.Err() == nil {
			applog.L(ctx).Error("coupon side effect dispatch failed", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Dispatcher) RecoverAndDispatchOnce(ctx context.Context) error {
	if d == nil || !d.cfg.Enabled {
		return nil
	}
	if _, err := d.repo.RecoverStaleProcessing(ctx, time.Now().UTC().Add(-d.cfg.StaleTimeout), d.cfg.BatchSize); err != nil {
		return err
	}
	items, err := d.repo.ListDispatchable(ctx, d.cfg.BatchSize)
	if err != nil {
		return err
	}
	for _, item := range items {
		acquired, markErr := d.repo.TryMarkProcessing(ctx, item.ID)
		if markErr != nil {
			applog.L(ctx).Warn("mark coupon side effect processing failed",
				zap.Int64("side_effect_id", item.ID),
				zap.Int64("claim_id", item.ClaimID),
				zap.Error(markErr),
			)
			continue
		}
		if !acquired {
			continue
		}
		if dispatchErr := d.dispatchOne(ctx, item); dispatchErr != nil {
			if item.RetryCount+1 >= d.cfg.MaxRetry {
				if markSuspendErr := d.repo.MarkSuspended(ctx, item.ID, "max retry boundary reached: "+dispatchErr.Error()); markSuspendErr != nil {
					applog.L(ctx).Warn("mark coupon side effect suspended failed",
						zap.Int64("side_effect_id", item.ID),
						zap.Int64("claim_id", item.ClaimID),
						zap.Error(markSuspendErr),
					)
				}
				applog.L(ctx).Warn("coupon side effect dispatch suspended",
					zap.Int64("side_effect_id", item.ID),
					zap.Int64("claim_id", item.ClaimID),
					zap.Error(dispatchErr),
				)
			} else {
				if markRetryErr := d.repo.MarkRetry(ctx, item.ID, d.cfg.RetryDelay, dispatchErr.Error()); markRetryErr != nil {
					applog.L(ctx).Warn("mark coupon side effect retry failed",
						zap.Int64("side_effect_id", item.ID),
						zap.Int64("claim_id", item.ClaimID),
						zap.Error(markRetryErr),
					)
				}
				applog.L(ctx).Warn("coupon side effect dispatch retry scheduled",
					zap.Int64("side_effect_id", item.ID),
					zap.Int64("claim_id", item.ClaimID),
					zap.Error(dispatchErr),
				)
			}
		}
	}
	return nil
}

func (d *Dispatcher) dispatchOne(ctx context.Context, item Record) error {
	payload, err := ParsePayload(item.PayloadJSON)
	if err != nil {
		return fmt.Errorf("parse claim side effect payload: %w", err)
	}

	taskPayload, err := task.MarshalPayload(task.SendCouponNoticePayload{
		ClaimID:  payload.ClaimID,
		CouponID: payload.CouponID,
		UserID:   payload.UserID,
		TraceID:  payload.TraceID,
	})
	if err != nil {
		return fmt.Errorf("marshal task payload: %w", err)
	}

	bizID := fmt.Sprintf("claim:%d", payload.ClaimID)
	asyncTask, err := d.taskRepo.Create(ctx, task.CreateTaskParams{
		TaskType: task.TaskTypeSendCouponNotice,
		BizID:    bizID,
		Payload:  taskPayload,
		MaxRetry: d.cfg.MaxRetry,
	})
	if err != nil {
		if err != task.ErrTaskDuplicate {
			return fmt.Errorf("create async task: %w", err)
		}
		asyncTask, err = d.taskRepo.GetByTypeBiz(ctx, task.TaskTypeSendCouponNotice, bizID)
		if err != nil {
			return fmt.Errorf("query duplicate async task: %w", err)
		}
	}

	aggregateID := fmt.Sprintf("%d", item.ID)
	evt, found, err := d.outbox.FindByAggregate(ctx, outbox.EventTypeTaskCreated, "claim_side_effect", aggregateID)
	if err != nil {
		return fmt.Errorf("query outbox by aggregate: %w", err)
	}
	if !found {
		eventPayload, marshalErr := outbox.MarshalTaskCreatedPayload(asyncTask.ID)
		if marshalErr != nil {
			return fmt.Errorf("marshal outbox payload: %w", marshalErr)
		}
		evt, err = d.outbox.Create(ctx, outbox.CreateEventParams{
			EventType:     outbox.EventTypeTaskCreated,
			AggregateType: "claim_side_effect",
			AggregateID:   aggregateID,
			PayloadJSON:   eventPayload,
		})
		if err != nil {
			return fmt.Errorf("create outbox event: %w", err)
		}
	}

	if err := d.repo.MarkDone(ctx, item.ID, asyncTask.ID, evt.ID); err != nil {
		return fmt.Errorf("mark side effect done: %w", err)
	}
	return nil
}
