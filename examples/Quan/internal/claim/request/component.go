package request

import (
	"context"
	"fmt"
	"sync"
	"time"

	applog "mini-jupiter/pkg/log"

	"go.uber.org/zap"
)

type ReconcilerConfig struct {
	PollInterval time.Duration
	BatchSize    int
}

func (c ReconcilerConfig) withDefaults() ReconcilerConfig {
	if c.PollInterval <= 0 {
		c.PollInterval = 2 * time.Second
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 100
	}
	return c
}

type ReconcilerComponent struct {
	reconciler *Reconciler
	cfg        ReconcilerConfig

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewReconcilerComponent(reconciler *Reconciler, cfg ReconcilerConfig) (*ReconcilerComponent, error) {
	if reconciler == nil {
		return nil, fmt.Errorf("claim request reconciler is nil")
	}
	return &ReconcilerComponent{reconciler: reconciler, cfg: cfg.withDefaults()}, nil
}

func (c *ReconcilerComponent) Start(ctx context.Context) error {
	loopCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.run(loopCtx)
	}()
	return nil
}

func (c *ReconcilerComponent) Stop(_ context.Context) error {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
	return nil
}

func (c *ReconcilerComponent) run(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.PollInterval)
	defer ticker.Stop()
	for {
		if err := c.reconciler.ReconcileOnce(ctx, c.cfg.BatchSize); err != nil && ctx.Err() == nil {
			applog.L(ctx).Warn("claim request reconcile failed", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
