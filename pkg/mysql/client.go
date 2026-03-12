package mysql

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/go-sql-driver/mysql"
)

type Client struct {
	raw *sql.DB
}

func NewClient(cfg Config) (*Client, error) {
	cfg = cfg.withDefaults()
	if cfg.DSN == "" {
		return nil, fmt.Errorf("mysql dsn is empty")
	}
	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	return &Client{raw: db}, nil
}

func (c *Client) Raw() *sql.DB {
	return c.raw
}

func (c *Client) Ping(ctx context.Context) error {
	return c.raw.PingContext(ctx)
}

func (c *Client) Close() error {
	return c.raw.Close()
}
