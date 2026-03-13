package task

import (
	"context"
	"fmt"
	"sync"
	"time"

	applog "mini-jupiter/pkg/log"

	"go.uber.org/zap"
)

type CompensationConfig struct {
	Enabled      bool          `mapstructure:"enabled" yaml:"enabled"`
	PollInterval time.Duration `mapstructure:"poll_interval" yaml:"poll_interval"`
	BatchSize    int           `mapstructure:"batch_size" yaml:"batch_size"`
	StaleTimeout time.Duration `mapstructure:"stale_timeout" yaml:"stale_timeout"`
}

func (c CompensationConfig) withDefaults() CompensationConfig {
	if c.PollInterval <= 0 {
		c.PollInterval = 1 * time.Second
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 100
	}
	if c.StaleTimeout <= 0 {
		c.StaleTimeout = 5 * time.Second
	}
	return c
}

type Compensator struct {
	cfg   CompensationConfig
	repo  compensationRepository
	queue compensationQueue

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type compensationRepository interface {
	ListDueFailedForCompensation(ctx context.Context, limit int) ([]int64, error)
	ListSuspendedForCompensation(ctx context.Context, staleBefore time.Time, limit int) ([]int64, error)
	ListStaleRunningForCompensation(ctx context.Context, staleBefore time.Time, limit int) ([]int64, error)
	MarkRecoveredForRetry(ctx context.Context, taskID int64, lastErr string) (bool, error)
}

type compensationQueue interface {
	ScheduleRetry(ctx context.Context, taskID int64, retryAt time.Time) error
}

func NewCompensator(repo compensationRepository, queue compensationQueue, cfg CompensationConfig) (*Compensator, error) {
	cfg = cfg.withDefaults()
	if !cfg.Enabled {
		return &Compensator{cfg: cfg}, nil
	}
	if repo == nil {
		return nil, fmt.Errorf("task compensator repository is nil")
	}
	if queue == nil {
		return nil, fmt.Errorf("task compensator queue is nil")
	}
	return &Compensator{
		cfg:   cfg,
		repo:  repo,
		queue: queue,
	}, nil
}

func (c *Compensator) Start(ctx context.Context) error {
	if c == nil || !c.cfg.Enabled {
		return nil
	}
	loopCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.run(loopCtx)
	}()
	return nil
}

func (c *Compensator) Stop(_ context.Context) error {
	if c == nil || !c.cfg.Enabled {
		return nil
	}
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
	return nil
}

func (c *Compensator) run(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.PollInterval)
	defer ticker.Stop()
	for {
		if err := c.compensateOnce(ctx); err != nil && ctx.Err() == nil {
			applog.L(ctx).Error("task compensation dispatch failed", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *Compensator) compensateOnce(ctx context.Context) error {
	taskIDs, err := c.repo.ListDueFailedForCompensation(ctx, c.cfg.BatchSize)
	if err != nil {
		return err
	}
	staleBefore := time.Now().UTC().Add(-c.cfg.StaleTimeout)
	suspendedIDs, err := c.repo.ListSuspendedForCompensation(ctx, staleBefore, c.cfg.BatchSize)
	if err != nil {
		return err
	}
	staleRunningIDs, err := c.repo.ListStaleRunningForCompensation(ctx, staleBefore, c.cfg.BatchSize)
	if err != nil {
		return err
	}
	if len(taskIDs) == 0 && len(suspendedIDs) == 0 && len(staleRunningIDs) == 0 {
		return nil
	}

	for _, taskID := range staleRunningIDs {
		ok, recoverErr := c.repo.MarkRecoveredForRetry(ctx, taskID, "stale RUNNING recovered for retry")
		if recoverErr != nil {
			applog.L(ctx).Warn("recover stale running task failed", zap.Int64("task_id", taskID), zap.Error(recoverErr))
			continue
		}
		if ok {
			taskIDs = append(taskIDs, taskID)
		}
	}
	for _, taskID := range suspendedIDs {
		ok, recoverErr := c.repo.MarkRecoveredForRetry(ctx, taskID, "suspended task recovered for retry")
		if recoverErr != nil {
			applog.L(ctx).Warn("recover suspended task failed", zap.Int64("task_id", taskID), zap.Error(recoverErr))
			continue
		}
		if ok {
			taskIDs = append(taskIDs, taskID)
		}
	}

	now := time.Now().UTC()
	recovered := 0
	for _, taskID := range taskIDs {
		if err := c.queue.ScheduleRetry(ctx, taskID, now); err != nil {
			applog.L(ctx).Warn("task compensation schedule retry failed", zap.Int64("task_id", taskID), zap.Error(err))
			continue
		}
		recovered++
	}
	if recovered > 0 {
		applog.L(ctx).Info("task compensation recovered due failed tasks", zap.Int("count", recovered))
	}
	return nil
}
