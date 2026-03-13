package outbox

import (
	"context"
	"fmt"
	"sync"
	"time"

	applog "mini-jupiter/pkg/log"

	"go.uber.org/zap"
)

type ReadyPublisher interface {
	PublishReady(ctx context.Context, taskID int64) error
}

type RelayConfig struct {
	Enabled         bool          `mapstructure:"enabled" yaml:"enabled"`
	PollInterval    time.Duration `mapstructure:"poll_interval" yaml:"poll_interval"`
	BatchSize       int           `mapstructure:"batch_size" yaml:"batch_size"`
	BackoffBase     time.Duration `mapstructure:"backoff_base" yaml:"backoff_base"`
	DispatchTimeout time.Duration `mapstructure:"dispatch_timeout" yaml:"dispatch_timeout"`
}

func (c RelayConfig) withDefaults() RelayConfig {
	if c.PollInterval <= 0 {
		c.PollInterval = 500 * time.Millisecond
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 100
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = 1 * time.Second
	}
	if c.DispatchTimeout <= 0 {
		c.DispatchTimeout = 5 * time.Second
	}
	return c
}

type Relay struct {
	cfg       RelayConfig
	repo      relayRepository
	publisher ReadyPublisher
	metrics   relayMetrics

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type relayRepository interface {
	ListDispatchable(ctx context.Context, limit int) ([]Event, error)
	CountPending(ctx context.Context) (int64, error)
	TryMarkDispatching(ctx context.Context, eventID int64) (bool, error)
	MarkPublished(ctx context.Context, eventID int64) error
	MarkRetry(ctx context.Context, eventID int64, delay time.Duration, lastErr string) error
	MarkSuspended(ctx context.Context, eventID int64, lastErr string) error
	RecoverStaleDispatching(ctx context.Context, staleBefore time.Time, limit int) (int64, error)
}

type relayMetrics interface {
	SetOutboxPending(v float64)
}

type noopRelayMetrics struct{}

func (noopRelayMetrics) SetOutboxPending(_ float64) {}

func NewRelay(repo relayRepository, publisher ReadyPublisher, cfg RelayConfig) (*Relay, error) {
	cfg = cfg.withDefaults()
	if !cfg.Enabled {
		return &Relay{cfg: cfg}, nil
	}
	if repo == nil {
		return nil, fmt.Errorf("outbox relay repository is nil")
	}
	if publisher == nil {
		return nil, fmt.Errorf("outbox relay publisher is nil")
	}
	return &Relay{
		cfg:       cfg,
		repo:      repo,
		publisher: publisher,
		metrics:   noopRelayMetrics{},
	}, nil
}

func (r *Relay) SetMetrics(metrics relayMetrics) {
	if r == nil || metrics == nil {
		return
	}
	r.metrics = metrics
}

func (r *Relay) Start(ctx context.Context) error {
	if r == nil || !r.cfg.Enabled {
		return nil
	}
	loopCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.run(loopCtx)
	}()
	return nil
}

func (r *Relay) Stop(_ context.Context) error {
	if r == nil || !r.cfg.Enabled {
		return nil
	}
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
	return nil
}

func (r *Relay) run(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()
	for {
		if cnt, err := r.repo.CountPending(ctx); err == nil {
			r.metrics.SetOutboxPending(float64(cnt))
		}
		if recovered, err := r.repo.RecoverStaleDispatching(ctx, time.Now().UTC().Add(-r.cfg.DispatchTimeout), r.cfg.BatchSize); err == nil && recovered > 0 {
			applog.L(ctx).Info("recovered stale dispatching outbox events", zap.Int64("count", recovered))
		}
		if err := r.dispatchOnce(ctx); err != nil {
			applog.L(ctx).Error("outbox relay dispatch failed", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Relay) dispatchOnce(ctx context.Context) error {
	events, err := r.repo.ListDispatchable(ctx, r.cfg.BatchSize)
	if err != nil {
		return err
	}
	for _, evt := range events {
		ok, markErr := r.repo.TryMarkDispatching(ctx, evt.ID)
		if markErr != nil {
			return markErr
		}
		if !ok {
			continue
		}
		payload, parseErr := ParseTaskCreatedPayload(evt.PayloadJSON)
		if parseErr != nil {
			_ = r.repo.MarkSuspended(ctx, evt.ID, "invalid outbox payload: "+parseErr.Error())
			continue
		}
		if payload.TaskID <= 0 {
			_ = r.repo.MarkSuspended(ctx, evt.ID, "invalid task_id in outbox payload")
			continue
		}
		if pubErr := r.publisher.PublishReady(ctx, payload.TaskID); pubErr != nil {
			_ = r.repo.MarkRetry(ctx, evt.ID, relayBackoff(evt.RetryCount, r.cfg.BackoffBase), pubErr.Error())
			continue
		}
		if markErr := r.repo.MarkPublished(ctx, evt.ID); markErr != nil {
			applog.L(ctx).Warn("mark outbox event published failed after publish; rely on stale-dispatch recovery",
				zap.Int64("event_id", evt.ID),
				zap.Int64("task_id", payload.TaskID),
				zap.Error(markErr),
			)
		}
	}
	return nil
}

func relayBackoff(retryCount int, base time.Duration) time.Duration {
	if retryCount < 0 {
		retryCount = 0
	}
	if retryCount > 8 {
		retryCount = 8
	}
	return base * time.Duration(1<<retryCount)
}
