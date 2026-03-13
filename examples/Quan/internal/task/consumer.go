package task

import (
	"context"
	"fmt"
	"sync"
	"time"

	applog "mini-jupiter/pkg/log"

	"go.uber.org/zap"
)

type ConsumeConfig struct {
	Enabled         bool          `mapstructure:"enabled" yaml:"enabled"`
	Workers         int           `mapstructure:"workers" yaml:"workers"`
	PollInterval    time.Duration `mapstructure:"poll_interval" yaml:"poll_interval"`
	ReadyTimeout    time.Duration `mapstructure:"ready_timeout" yaml:"ready_timeout"`
	RetryBackoff    time.Duration `mapstructure:"retry_backoff" yaml:"retry_backoff"`
	RetryMoveBatch  int           `mapstructure:"retry_move_batch" yaml:"retry_move_batch"`
	DefaultMaxRetry int           `mapstructure:"max_retry" yaml:"max_retry"`
}

func (c ConsumeConfig) withDefaults() ConsumeConfig {
	if c.Workers <= 0 {
		c.Workers = 4
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 200 * time.Millisecond
	}
	if c.ReadyTimeout <= 0 {
		c.ReadyTimeout = 1 * time.Second
	}
	if c.RetryBackoff <= 0 {
		c.RetryBackoff = 2 * time.Second
	}
	if c.RetryMoveBatch <= 0 {
		c.RetryMoveBatch = 100
	}
	if c.DefaultMaxRetry <= 0 {
		c.DefaultMaxRetry = 5
	}
	return c
}

type Consumer struct {
	cfg      ConsumeConfig
	repo     consumerRepository
	queue    consumerQueue
	registry *HandlerRegistry
	metrics  consumerMetrics

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type consumerRepository interface {
	TryMarkRunning(ctx context.Context, taskID int64) (AsyncTask, bool, error)
	MarkFailed(ctx context.Context, taskID int64, lastErr string, backoffBase time.Duration) (bool, *time.Time, error)
	MarkSuccess(ctx context.Context, taskID int64) error
	MarkSuspended(ctx context.Context, taskID int64, lastErr string) error
}

type consumerQueue interface {
	MoveDueRetryToReady(ctx context.Context, batch int) (int, error)
	PopReady(ctx context.Context, timeout time.Duration) (int64, bool, error)
	PushDLQ(ctx context.Context, taskID int64) error
	ScheduleRetry(ctx context.Context, taskID int64, retryAt time.Time) error
}

type consumerMetrics interface {
	IncTaskRetry()
	IncTaskDLQ()
	IncConsumeSuccess()
	IncConsumeFailure()
}

type noopConsumerMetrics struct{}

func (noopConsumerMetrics) IncTaskRetry()      {}
func (noopConsumerMetrics) IncTaskDLQ()        {}
func (noopConsumerMetrics) IncConsumeSuccess() {}
func (noopConsumerMetrics) IncConsumeFailure() {}

func NewConsumer(repo consumerRepository, queue consumerQueue, registry *HandlerRegistry, cfg ConsumeConfig) (*Consumer, error) {
	cfg = cfg.withDefaults()
	if !cfg.Enabled {
		return &Consumer{cfg: cfg}, nil
	}
	if repo == nil {
		return nil, fmt.Errorf("task consumer repository is nil")
	}
	if queue == nil {
		return nil, fmt.Errorf("task consumer queue is nil")
	}
	if registry == nil {
		return nil, fmt.Errorf("task consumer registry is nil")
	}
	return &Consumer{
		cfg:      cfg,
		repo:     repo,
		queue:    queue,
		registry: registry,
		metrics:  noopConsumerMetrics{},
	}, nil
}

func (c *Consumer) SetMetrics(metrics consumerMetrics) {
	if c == nil || metrics == nil {
		return
	}
	c.metrics = metrics
}

func (c *Consumer) Start(ctx context.Context) error {
	if c == nil || !c.cfg.Enabled {
		return nil
	}
	loopCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.runRetryScheduler(loopCtx)
	}()

	for i := 0; i < c.cfg.Workers; i++ {
		workerID := i + 1
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.runWorker(loopCtx, workerID)
		}()
	}
	return nil
}

func (c *Consumer) Stop(_ context.Context) error {
	if c == nil || !c.cfg.Enabled {
		return nil
	}
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
	return nil
}

func (c *Consumer) runRetryScheduler(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.PollInterval)
	defer ticker.Stop()

	for {
		moved, err := c.queue.MoveDueRetryToReady(ctx, c.cfg.RetryMoveBatch)
		if err != nil && ctx.Err() == nil {
			applog.L(ctx).Error("move retry tasks to ready failed", zap.Error(err))
		}
		if moved > 0 {
			applog.L(ctx).Info("moved retry tasks to ready", zap.Int("count", moved))
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *Consumer) runWorker(ctx context.Context, workerID int) {
	for {
		taskID, ok, err := c.queue.PopReady(ctx, c.cfg.ReadyTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			applog.L(ctx).Error("pop ready task failed", zap.Int("worker_id", workerID), zap.Error(err))
			continue
		}
		if !ok {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		if consumeErr := c.consumeTask(ctx, taskID, workerID); consumeErr != nil {
			applog.L(ctx).Error("consume task failed", zap.Int("worker_id", workerID), zap.Int64("task_id", taskID), zap.Error(consumeErr))
		}
	}
}

func (c *Consumer) consumeTask(ctx context.Context, taskID int64, workerID int) error {
	task, ok, err := c.repo.TryMarkRunning(ctx, taskID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	if err := c.registry.Handle(ctx, task); err != nil {
		c.metrics.IncConsumeFailure()
		dead, nextRetry, markErr := c.repo.MarkFailed(ctx, task.ID, err.Error(), c.cfg.RetryBackoff)
		if markErr != nil {
			return markErr
		}
		if dead {
			c.metrics.IncTaskDLQ()
			if pushErr := c.queue.PushDLQ(ctx, task.ID); pushErr != nil {
				return fmt.Errorf("push task to dlq failed: %w", pushErr)
			}
			applog.L(ctx).Info("task marked dead", zap.Int("worker_id", workerID), zap.Int64("task_id", task.ID))
			return nil
		}
		if nextRetry != nil {
			c.metrics.IncTaskRetry()
			if scheduleErr := c.queue.ScheduleRetry(ctx, task.ID, *nextRetry); scheduleErr != nil {
				return fmt.Errorf("schedule task retry failed: %w", scheduleErr)
			}
		}
		return nil
	}

	if err := c.repo.MarkSuccess(ctx, task.ID); err != nil {
		suspendErr := c.repo.MarkSuspended(ctx, task.ID, "handler succeeded but mark success failed: "+err.Error())
		if suspendErr != nil {
			return fmt.Errorf("mark task success failed: %w; suspend fallback failed: %v", err, suspendErr)
		}
		return err
	}
	c.metrics.IncConsumeSuccess()
	applog.L(ctx).Info("task consumed successfully", zap.Int("worker_id", workerID), zap.Int64("task_id", task.ID))
	return nil
}
