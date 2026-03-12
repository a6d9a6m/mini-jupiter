package redis

import (
	"context"
	"fmt"
	"time"
)

type Component struct {
	cfg    Config
	client *Client
}

func NewComponent(cfg Config) (*Component, error) {
	if !cfg.Enabled {
		return nil, fmt.Errorf("redis is disabled")
	}
	client, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return &Component{cfg: cfg, client: client}, nil
}

func (c *Component) Client() *Client {
	return c.client
}

func (c *Component) Start(ctx context.Context) error {
	ctx, cancel := withTimeoutIfNone(ctx, c.cfg.withDefaults().DialTimeout)
	defer cancel()
	return c.client.Ping(ctx)
}

func (c *Component) Stop(_ context.Context) error {
	return c.client.Close()
}

func withTimeoutIfNone(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}
