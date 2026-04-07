package redis

import "time"

const (
	ModeSimple   = "simple"
	ModeSentinel = "sentinel"
)

type Config struct {
	Enabled          bool          `mapstructure:"enabled" yaml:"enabled"`
	Mode             string        `mapstructure:"mode" yaml:"mode"`
	Addr             string        `mapstructure:"addr" yaml:"addr"`
	Addrs            []string      `mapstructure:"addrs" yaml:"addrs"`
	MasterName       string        `mapstructure:"master_name" yaml:"master_name"`
	Password         string        `mapstructure:"password" yaml:"password"`
	SentinelUsername string        `mapstructure:"sentinel_username" yaml:"sentinel_username"`
	SentinelPassword string        `mapstructure:"sentinel_password" yaml:"sentinel_password"`
	DB               int           `mapstructure:"db" yaml:"db"`
	DialTimeout      time.Duration `mapstructure:"dial_timeout" yaml:"dial_timeout"`
}

func (c Config) withDefaults() Config {
	if c.Mode == "" {
		if c.MasterName != "" || len(c.Addrs) > 0 {
			c.Mode = ModeSentinel
		} else {
			c.Mode = ModeSimple
		}
	}
	if c.Addr == "" {
		c.Addr = "127.0.0.1:6379"
	}
	if len(c.Addrs) == 0 {
		if c.Mode == ModeSentinel {
			c.Addrs = []string{"127.0.0.1:26379"}
		} else {
			c.Addrs = []string{c.Addr}
		}
	}
	if c.MasterName == "" && c.Mode == ModeSentinel {
		c.MasterName = "mymaster"
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = 2 * time.Second
	}
	return c
}
