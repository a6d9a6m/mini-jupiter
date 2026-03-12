package redis

import (
	"context"
	"fmt"

	goredis "github.com/redis/go-redis/v9"
)

type Client struct {
	raw *goredis.Client
}

func NewClient(cfg Config) (*Client, error) {
	cfg = cfg.withDefaults()
	if cfg.Addr == "" {
		return nil, fmt.Errorf("redis addr is empty")
	}
	c := goredis.NewClient(&goredis.Options{
		Addr:        cfg.Addr,
		Password:    cfg.Password,
		DB:          cfg.DB,
		DialTimeout: cfg.DialTimeout,
	})
	return &Client{raw: c}, nil
}

func (c *Client) Raw() *goredis.Client {
	return c.raw
}

func (c *Client) Ping(ctx context.Context) error {
	return c.raw.Ping(ctx).Err()
}

func (c *Client) Close() error {
	return c.raw.Close()
}
