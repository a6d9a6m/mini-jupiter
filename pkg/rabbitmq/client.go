package rabbitmq

import (
	"context"
	"fmt"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"
)

type Client struct {
	mu     sync.Mutex
	conn   *amqp.Connection
	cfg    Config
	closed bool
}

func NewClient(cfg Config) (*Client, error) {
	cfg = cfg.withDefaults()
	client := &Client{cfg: cfg}
	conn, err := client.dial()
	if err != nil {
		return nil, err
	}
	client.conn = conn
	return client, nil
}

func (c *Client) Raw() *amqp.Connection {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn
}

func (c *Client) Channel() (*amqp.Channel, error) {
	if c == nil {
		return nil, fmt.Errorf("rabbitmq client is nil")
	}
	conn, err := c.ensureConnection()
	if err != nil {
		return nil, err
	}
	ch, err := conn.Channel()
	if err == nil {
		return ch, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("rabbitmq client is closed")
	}
	if c.conn != nil && c.conn.IsClosed() {
		if c.conn, err = c.dial(); err != nil {
			return nil, err
		}
		return c.conn.Channel()
	}
	return nil, err
}

func (c *Client) Ping(_ context.Context) error {
	ch, err := c.Channel()
	if err != nil {
		return err
	}
	return ch.Close()
}

func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *Client) ensureConnection() (*amqp.Connection, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("rabbitmq client is closed")
	}
	if c.conn != nil && !c.conn.IsClosed() {
		return c.conn, nil
	}
	conn, err := c.dial()
	if err != nil {
		return nil, err
	}
	c.conn = conn
	return c.conn, nil
}

func (c *Client) dial() (*amqp.Connection, error) {
	return amqp.DialConfig(c.cfg.URL, amqp.Config{
		Heartbeat: c.cfg.Heartbeat,
		Dial:      amqp.DefaultDial(c.cfg.DialTimeout),
	})
}
