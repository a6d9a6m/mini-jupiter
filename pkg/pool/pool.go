package pool

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrClosed = errors.New("worker pool closed")

type Task func(context.Context) error

type Pool struct {
	workers     int
	tasks       chan Task
	wg          sync.WaitGroup
	mu          sync.Mutex
	closed      bool
	taskTimeout time.Duration
}

type Option func(*Pool)

func New(workers int, opts ...Option) *Pool {
	p := &Pool{
		workers:     workers,
		tasks:       make(chan Task, 1024),
		taskTimeout: 10 * time.Second,
	}
	for _, opt := range opts {
		opt(p)
	}
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go p.worker()
	}
	return p
}

func WithBuffer(size int) Option {
	return func(p *Pool) {
		if size > 0 {
			p.tasks = make(chan Task, size)
		}
	}
}

func WithTaskTimeout(d time.Duration) Option {
	return func(p *Pool) {
		if d > 0 {
			p.taskTimeout = d
		}
	}
}

func (p *Pool) Submit(ctx context.Context, task Task) error {
	if task == nil {
		return errors.New("task is nil")
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrClosed
	}
	p.mu.Unlock()

	select {
	case p.tasks <- task:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	close(p.tasks)
	p.mu.Unlock()
	p.wg.Wait()
}

func (p *Pool) worker() {
	defer p.wg.Done()
	for task := range p.tasks {
		ctx, cancel := context.WithTimeout(context.Background(), p.taskTimeout)
		_ = task(ctx)
		cancel()
	}
}
