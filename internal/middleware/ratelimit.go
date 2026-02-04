package middleware

import (
	"net/http"

	apperr "mini-jupiter/pkg/errors"
	"mini-jupiter/pkg/ratelimiter"
)

func RateLimit(l *ratelimiter.Limiter) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if l != nil && !l.Allow() {
				apperr.WriteHTTPWithContext(r.Context(), w, apperr.New(apperr.CodeTooManyRequests, "too many requests"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
