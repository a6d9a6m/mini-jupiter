package middleware

import (
	"net/http"

	applog "mini-jupiter/pkg/log"

	"go.uber.org/zap"
)

func Recovery() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					applog.L(r.Context()).Error("panic recovered",
						zap.Any("panic", rec),
						zap.String("path", r.URL.Path),
						zap.String("method", r.Method),
					)
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte("internal server error"))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
