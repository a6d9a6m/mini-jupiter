package isolation

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

var ErrRejected = errors.New("request rejected")

type RouteConfig struct {
	MaxConcurrent int `mapstructure:"max_concurrent" yaml:"max_concurrent"`
	MaxQueue      int `mapstructure:"max_queue" yaml:"max_queue"`
	WaitTimeoutMs int `mapstructure:"wait_timeout_ms" yaml:"wait_timeout_ms"`
}

type Config struct {
	Enabled bool                   `mapstructure:"enabled" yaml:"enabled"`
	Routes  map[string]RouteConfig `mapstructure:"routes" yaml:"routes"`
}

type Limiter struct {
	sem        chan struct{}
	maxQueue   int64
	queued     atomic.Int64
	waitTimeout time.Duration
}

func NewLimiter(maxConcurrent, maxQueue int, waitTimeout time.Duration) *Limiter {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	if maxQueue < 0 {
		maxQueue = 0
	}
	if waitTimeout <= 0 {
		waitTimeout = 50 * time.Millisecond
	}
	return &Limiter{
		sem:         make(chan struct{}, maxConcurrent),
		maxQueue:    int64(maxQueue),
		waitTimeout: waitTimeout,
	}
}

func (l *Limiter) Acquire(ctx context.Context) (func(), error) {
	select {
	case l.sem <- struct{}{}:
		return func() { <-l.sem }, nil
	default:
	}

	if l.maxQueue == 0 {
		return nil, ErrRejected
	}

	if l.queued.Add(1) > l.maxQueue {
		l.queued.Add(-1)
		return nil, ErrRejected
	}
	defer l.queued.Add(-1)

	timer := time.NewTimer(l.waitTimeout)
	defer timer.Stop()

	select {
	case l.sem <- struct{}{}:
		return func() { <-l.sem }, nil
	case <-timer.C:
		return nil, ErrRejected
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type Manager struct {
	mu       sync.RWMutex
	limiters map[string]*Limiter
}

func NewManager(cfg Config) *Manager {
	m := &Manager{limiters: make(map[string]*Limiter)}
	for route, rc := range cfg.Routes {
		m.limiters[route] = NewLimiter(rc.MaxConcurrent, rc.MaxQueue, time.Duration(rc.WaitTimeoutMs)*time.Millisecond)
	}
	return m
}

func (m *Manager) Limiter(path string) *Limiter {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.limiters[path]
}
