package runtime

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func WaitSignal(ctx context.Context, signals ...os.Signal) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(signals) == 0 {
		signals = []os.Signal{syscall.SIGINT, syscall.SIGTERM}
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, signals...)
	defer signal.Stop(ch)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-ch:
		return nil
	}
}
