package redis

import "time"

type Config struct {
	Enabled     bool          `mapstructure:"enabled" yaml:"enabled"`
	Addr        string        `mapstructure:"addr" yaml:"addr"`
	Password    string        `mapstructure:"password" yaml:"password"`
	DB          int           `mapstructure:"db" yaml:"db"`
	DialTimeout time.Duration `mapstructure:"dial_timeout" yaml:"dial_timeout"`
}

func (c Config) withDefaults() Config {
	if c.Addr == "" {
		c.Addr = "127.0.0.1:6379"
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = 2 * time.Second
	}
	return c
}
