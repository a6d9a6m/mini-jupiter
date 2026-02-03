package log

import (
	"context"

	"go.uber.org/zap"
)

type traceIDKey struct{}

func WithTraceID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, traceIDKey{}, id)
}

func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if val := ctx.Value(traceIDKey{}); val != nil {
		if s, ok := val.(string); ok {
			return s
		}
	}
	return ""
}

func L(ctx context.Context) *zap.Logger {
	traceID := TraceIDFromContext(ctx)
	if traceID == "" {
		return zap.L()
	}
	return zap.L().With(zap.String("trace_id", traceID))
}

func S(ctx context.Context) *zap.SugaredLogger {
	return L(ctx).Sugar()
}
