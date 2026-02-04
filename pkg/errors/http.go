package errors

import (
	"context"
	"encoding/json"
	"net/http"

	applog "mini-jupiter/pkg/log"
)

func HTTPStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if e, ok := err.(*Error); ok {
		switch e.Code {
		case CodeBadRequest:
			return http.StatusBadRequest
		case CodeTooManyRequests:
			return http.StatusTooManyRequests
		case CodeNotFound:
			return http.StatusNotFound
		default:
			return http.StatusInternalServerError
		}
	}
	return http.StatusInternalServerError
}

type Response struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	TraceID string `json:"trace_id,omitempty"`
}

func WriteHTTP(w http.ResponseWriter, err error) {
	WriteHTTPWithContext(context.Background(), w, err)
}

func WriteHTTPWithContext(ctx context.Context, w http.ResponseWriter, err error) {
	status := HTTPStatus(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err == nil {
		return
	}
	msg := "internal error"
	code := CodeInternalError
	if e, ok := err.(*Error); ok {
		msg = e.Message
		code = e.Code
	}
	traceID := applog.TraceIDFromContext(ctx)
	report(code)
	_ = json.NewEncoder(w).Encode(Response{
		Code:    code,
		Message: msg,
		TraceID: traceID,
	})
}
