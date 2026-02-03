package runtime

import "context"

type Component interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}
