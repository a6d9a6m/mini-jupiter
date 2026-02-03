package ratelimiter

import (
	"sync"
	"time"
)

type Config struct {
	Enabled bool    `mapstructure:"enabled" yaml:"enabled"`
	Rate    float64 `mapstructure:"rate" yaml:"rate"`
	Burst   int     `mapstructure:"burst" yaml:"burst"`
}

type Limiter struct {
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
	mu     sync.Mutex
}

func New(rate float64, burst int) *Limiter {
	if rate <= 0 {
		rate = 1
	}
	if burst <= 0 {
		burst = 1
	}
	now := time.Now()
	return &Limiter{
		rate:   rate,
		burst: float64(burst),
		tokens: float64(burst),
		last:   now,
	}
}

func (l *Limiter) Allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(l.last).Seconds()
	l.last = now

	l.tokens += elapsed * l.rate
	if l.tokens > l.burst {
		l.tokens = l.burst
	}
	if l.tokens < 1 {
		return false
	}
	l.tokens -= 1
	return true
}
