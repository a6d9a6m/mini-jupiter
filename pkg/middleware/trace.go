package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"

	applog "mini-jupiter/pkg/log"
)

const traceHeader = "X-Trace-Id"

func TraceID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			traceID := r.Header.Get(traceHeader)
			if traceID == "" {
				traceID = newTraceID()
			}
			ctx := applog.WithTraceID(r.Context(), traceID)
			w.Header().Set(traceHeader, traceID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func newTraceID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "trace-unknown"
	}
	return hex.EncodeToString(b[:])
}
