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
	cfg     CompensationConfig
	repo    compensationRepository
	queue   compensationQueue
	metrics compensationMetrics

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type compensationRepository interface {
	ListDueFailedForCompensation(ctx context.Context, limit int) ([]RecoveryCandidate, error)
	ListSuspendedForCompensation(ctx context.Context, staleBefore time.Time, limit int) ([]RecoveryCandidate, error)
	ListStaleRunningForCompensation(ctx context.Context, staleBefore time.Time, limit int) ([]RecoveryCandidate, error)
	MarkRecoveredForRetry(ctx context.Context, taskID int64, expectedVersion int64, lastErr string) (bool, error)
}

type compensationQueue interface {
	ScheduleRetry(ctx context.Context, taskID int64, retryAt time.Time) error
}

type compensationMetrics interface {
	ObserveTaskRecovery(source string, latency time.Duration)
}

type noopCompensationMetrics struct{}

func (noopCompensationMetrics) ObserveTaskRecovery(string, time.Duration) {}

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
		cfg:     cfg,
		repo:    repo,
		queue:   queue,
		metrics: noopCompensationMetrics{},
	}, nil
}

func (c *Compensator) SetMetrics(metrics compensationMetrics) {
	if c == nil || metrics == nil {
		return
	}
	c.metrics = metrics
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
	candidates, err := c.repo.ListDueFailedForCompensation(ctx, c.cfg.BatchSize)
	if err != nil {
		return err
	}
	staleBefore := time.Now().UTC().Add(-c.cfg.StaleTimeout)
	suspendedCandidates, err := c.repo.ListSuspendedForCompensation(ctx, staleBefore, c.cfg.BatchSize)
	if err != nil {
		return err
	}
	staleRunningCandidates, err := c.repo.ListStaleRunningForCompensation(ctx, staleBefore, c.cfg.BatchSize)
	if err != nil {
		return err
	}
	if len(candidates) == 0 && len(suspendedCandidates) == 0 && len(staleRunningCandidates) == 0 {
		return nil
	}

	for _, candidate := range staleRunningCandidates {
		ok, recoverErr := c.repo.MarkRecoveredForRetry(ctx, candidate.TaskID, candidate.Version, "stale RUNNING recovered for retry")
		if recoverErr != nil {
			applog.L(ctx).Warn("recover stale running task failed", zap.Int64("task_id", candidate.TaskID), zap.Error(recoverErr))
			continue
		}
		if ok {
			candidates = append(candidates, candidate)
		}
	}
	for _, candidate := range suspendedCandidates {
		ok, recoverErr := c.repo.MarkRecoveredForRetry(ctx, candidate.TaskID, candidate.Version, "suspended task recovered for retry")
		if recoverErr != nil {
			applog.L(ctx).Warn("recover suspended task failed", zap.Int64("task_id", candidate.TaskID), zap.Error(recoverErr))
			continue
		}
		if ok {
			candidates = append(candidates, candidate)
		}
	}

	now := time.Now().UTC()
	recovered := 0
	for _, candidate := range candidates {
		if err := c.queue.ScheduleRetry(ctx, candidate.TaskID, now); err != nil {
			applog.L(ctx).Warn("task compensation schedule retry failed", zap.Int64("task_id", candidate.TaskID), zap.Error(err))
			continue
		}
		c.metrics.ObserveTaskRecovery(candidate.Source, now.Sub(candidate.RecoverAt))
		recovered++
	}
	if recovered > 0 {
		applog.L(ctx).Info("task compensation recovered due failed tasks", zap.Int("count", recovered))
	}
	return nil
}
