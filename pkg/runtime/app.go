package runtime

import (
	"context"
	"errors"
	"sync"
	"time"
)

type App struct {
	components []Component
	stopTimeout time.Duration
}

type Option func(*App)

func WithStopTimeout(d time.Duration) Option {
	return func(a *App) {
		a.stopTimeout = d
	}
}

func New(components ...Component) *App {
	return &App{
		components:  components,
		stopTimeout: 10 * time.Second,
	}
}

func NewWithOptions(opts ...Option) *App {
	a := New()
	for _, opt := range opts {
		opt(a)
	}
	return a
}

func (a *App) Use(components ...Component) {
	a.components = append(a.components, components...)
}

func (a *App) Start(ctx context.Context) error {
	var started []Component
	for _, c := range a.components {
		if err := c.Start(ctx); err != nil {
			_ = a.stopReverse(ctx, started)
			return err
		}
		started = append(started, c)
	}
	return nil
}

func (a *App) Stop(ctx context.Context) error {
	return a.stopReverse(ctx, a.components)
}

func (a *App) stopReverse(ctx context.Context, comps []Component) error {
	ctx = withDefaultTimeout(ctx, a.stopTimeout)
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		errs []error
	)
	for i := len(comps) - 1; i >= 0; i-- {
		c := comps[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.Stop(ctx); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func withDefaultTimeout(ctx context.Context, d time.Duration) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx
	}
	c, _ := context.WithTimeout(ctx, d)
	return c
}
