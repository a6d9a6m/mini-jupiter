package middleware

import (
	"net/http"

	apperr "mini-jupiter/pkg/errors"
	"mini-jupiter/pkg/isolation"
)

func Isolation(mgr *isolation.Manager) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if mgr == nil {
				next.ServeHTTP(w, r)
				return
			}
			limiter := mgr.Limiter(r.URL.Path)
			if limiter == nil {
				next.ServeHTTP(w, r)
				return
			}
			release, err := limiter.Acquire(r.Context())
			if err != nil {
				apperr.WriteHTTPWithContext(r.Context(), w, apperr.New(apperr.CodeTooManyRequests, "request rejected"))
				return
			}
			defer release()
			next.ServeHTTP(w, r)
		})
	}
}
