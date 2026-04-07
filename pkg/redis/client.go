package redis

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

type Client struct {
	raw *goredis.Client
}

func NewClient(cfg Config) (*Client, error) {
	cfg = cfg.withDefaults()
	switch cfg.Mode {
	case ModeSimple:
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
	case ModeSentinel:
		if cfg.MasterName == "" {
			return nil, fmt.Errorf("redis sentinel master_name is empty")
		}
		if len(cfg.Addrs) == 0 {
			return nil, fmt.Errorf("redis sentinel addrs are empty")
		}
		c := goredis.NewFailoverClient(&goredis.FailoverOptions{
			MasterName:       cfg.MasterName,
			SentinelAddrs:    cfg.Addrs,
			Password:         cfg.Password,
			SentinelUsername: cfg.SentinelUsername,
			SentinelPassword: cfg.SentinelPassword,
			DB:               cfg.DB,
			DialTimeout:      cfg.DialTimeout,
		})
		return &Client{raw: c}, nil
	default:
		return nil, fmt.Errorf("unsupported redis mode: %s", cfg.Mode)
	}
}

func (c *Client) Raw() *goredis.Client {
	return c.raw
}

func (c *Client) Ping(ctx context.Context) error {
	return c.raw.Ping(ctx).Err()
}

func (c *Client) Wait(ctx context.Context, replicas int, timeout time.Duration) (int64, error) {
	if c == nil || c.raw == nil {
		return 0, fmt.Errorf("redis client is nil")
	}
	if replicas < 0 {
		replicas = 0
	}
	if timeout < 0 {
		timeout = 0
	}
	return c.raw.Wait(ctx, replicas, timeout).Result()
}

func (c *Client) Close() error {
	return c.raw.Close()
}
