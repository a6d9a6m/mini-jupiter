package middleware

import (
	"net/http"
	"time"

	applog "mini-jupiter/pkg/log"
	"mini-jupiter/pkg/metric"

	"go.uber.org/zap"
)

func Logging(m *metric.Metrics) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if m != nil {
				m.IncInFlight(r.Method, r.URL.Path)
				defer m.DecInFlight(r.Method, r.URL.Path)
			}
			start := time.Now()
			rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			cost := time.Since(start)
			if m != nil {
				m.Observe(r.Method, r.URL.Path, rec.status, cost.Seconds())
			}
			applog.L(r.Context()).Info("http request",
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.Int("status", rec.status),
				zap.Duration("cost", cost),
			)
		})
	}
}

type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (r *responseRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
