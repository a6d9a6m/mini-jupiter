package rabbitmq

import "time"

type Config struct {
	Enabled     bool          `mapstructure:"enabled" yaml:"enabled"`
	URL         string        `mapstructure:"url" yaml:"url"`
	DialTimeout time.Duration `mapstructure:"dial_timeout" yaml:"dial_timeout"`
	Heartbeat   time.Duration `mapstructure:"heartbeat" yaml:"heartbeat"`
}

func (c Config) withDefaults() Config {
	if c.URL == "" {
		c.URL = "amqp://guest:guest@127.0.0.1:5672/"
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = 5 * time.Second
	}
	if c.Heartbeat <= 0 {
		c.Heartbeat = 10 * time.Second
	}
	return c
}
