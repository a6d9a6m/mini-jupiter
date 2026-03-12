package mysql

import "time"

type Config struct {
	Enabled         bool          `mapstructure:"enabled" yaml:"enabled"`
	DSN             string        `mapstructure:"dsn" yaml:"dsn"`
	MaxOpenConns    int           `mapstructure:"max_open_conns" yaml:"max_open_conns"`
	MaxIdleConns    int           `mapstructure:"max_idle_conns" yaml:"max_idle_conns"`
	ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime" yaml:"conn_max_lifetime"`
	ConnTimeout     time.Duration `mapstructure:"conn_timeout" yaml:"conn_timeout"`
}

func (c Config) withDefaults() Config {
	if c.MaxOpenConns <= 0 {
		c.MaxOpenConns = 50
	}
	if c.MaxIdleConns < 0 {
		c.MaxIdleConns = 0
	}
	if c.MaxIdleConns > c.MaxOpenConns {
		c.MaxIdleConns = c.MaxOpenConns
	}
	if c.ConnMaxLifetime <= 0 {
		c.ConnMaxLifetime = 30 * time.Minute
	}
	if c.ConnTimeout <= 0 {
		c.ConnTimeout = 2 * time.Second
	}
	return c
}
