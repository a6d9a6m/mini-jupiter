package claimapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	claimrequest "mini-jupiter/examples/Quan/internal/claim/request"
	apperr "mini-jupiter/pkg/errors"
	applog "mini-jupiter/pkg/log"
)

const (
	headerUserID         = "X-User-ID"
	headerIdempotencyKey = "Idempotency-Key"
)

type claimService interface {
	Accept(ctx context.Context, req claimrequest.AcceptRequest) (claimrequest.AcceptResponse, error)
	Get(ctx context.Context, requestID string) (claimrequest.QueryResult, error)
}

type Handler struct {
	svc     claimService
	metrics claimMetrics
}

func NewHandler(svc claimService) *Handler {
	return &Handler{svc: svc}
}

type claimMetrics interface {
	ObserveClaimRequestAccept(resultClass, resultCode string, duration time.Duration)
}

func (h *Handler) SetMetrics(metrics claimMetrics) {
	if h == nil || metrics == nil {
		return
	}
	h.metrics = metrics
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/coupons/{coupon_id}/claim", h.claimCoupon)
	mux.HandleFunc("GET /api/v1/claim-requests/{request_id}", h.getRequest)
}

func (h *Handler) claimCoupon(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	userID, ok := parseUserID(r)
	if !ok {
		h.observeClaim("client_error", "bad_request", time.Since(start))
		apperr.WriteHTTPWithContext(r.Context(), w, apperr.New(apperr.CodeBadRequest, "invalid X-User-ID"))
		return
	}
	couponID, ok := parsePathInt64(r.PathValue("coupon_id"))
	if !ok {
		h.observeClaim("client_error", "bad_request", time.Since(start))
		apperr.WriteHTTPWithContext(r.Context(), w, apperr.New(apperr.CodeBadRequest, "invalid coupon_id"))
		return
	}
	idemKey := strings.TrimSpace(r.Header.Get(headerIdempotencyKey))
	if idemKey == "" {
		h.observeClaim("client_error", "bad_request", time.Since(start))
		apperr.WriteHTTPWithContext(r.Context(), w, apperr.New(apperr.CodeBadRequest, "missing Idempotency-Key"))
		return
	}

	accepted, err := h.svc.Accept(r.Context(), claimrequest.AcceptRequest{
		CouponID:       couponID,
		UserID:         userID,
		IdempotencyKey: idemKey,
	})
	if err != nil {
		h.observeClaim(classifyClaimResult(err), classifyClaimCode(err), time.Since(start))
		apperr.WriteHTTPWithContext(r.Context(), w, err)
		return
	}
	h.observeClaim("success", "accepted", time.Since(start))

	writeAccepted(r.Context(), w, map[string]any{
		"request_id": accepted.RequestID,
		"status":     accepted.Status,
	})
}

func (h *Handler) observeClaim(resultClass, resultCode string, duration time.Duration) {
	if h == nil || h.metrics == nil {
		return
	}
	h.metrics.ObserveClaimRequestAccept(resultClass, resultCode, duration)
}

func classifyClaimResult(err error) string {
	if err == nil {
		return "success"
	}
	status := apperr.HTTPStatus(err)
	switch status {
	case http.StatusConflict:
		return "business_conflict"
	case http.StatusBadRequest, http.StatusNotFound, http.StatusTooManyRequests:
		return "client_error"
	default:
		return "server_error"
	}
}

func classifyClaimCode(err error) string {
	if err == nil {
		return "success"
	}
	if e, ok := err.(*apperr.Error); ok {
		switch e.Code {
		case apperr.CodeConflict:
			switch e.Message {
			case "already claimed":
				return "already_claimed"
			case "claim limit reached":
				return "limit_reached"
			case "coupon sold out":
				return "sold_out"
			default:
				return "conflict"
			}
		case apperr.CodeBadRequest:
			return "bad_request"
		case apperr.CodeNotFound:
			return "not_found"
		case apperr.CodeTooManyRequests:
			return "too_many_requests"
		case apperr.CodeInternalError:
			return "internal_error"
		}
	}
	return "internal_error"
}

func (h *Handler) getRequest(w http.ResponseWriter, r *http.Request) {
	requestID := strings.TrimSpace(r.PathValue("request_id"))
	if requestID == "" {
		apperr.WriteHTTPWithContext(r.Context(), w, apperr.New(apperr.CodeBadRequest, "invalid request_id"))
		return
	}
	result, err := h.svc.Get(r.Context(), requestID)
	if err != nil {
		apperr.WriteHTTPWithContext(r.Context(), w, err)
		return
	}
	writeOK(r.Context(), w, map[string]any{
		"request_id":   result.RequestID,
		"state":        result.State,
		"internal":     result.Internal,
		"claim_id":     result.ClaimID,
		"failure_code": result.FailureCode,
	})
}

func parseUserID(r *http.Request) (int64, bool) {
	return parsePathInt64(strings.TrimSpace(r.Header.Get(headerUserID)))
}

func parsePathInt64(raw string) (int64, bool) {
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

func writeOK(ctx context.Context, w http.ResponseWriter, data any) {
	traceID := applog.TraceIDFromContext(ctx)
	resp := map[string]any{
		"code":    0,
		"message": "ok",
		"data":    data,
	}
	if traceID != "" {
		resp["trace_id"] = traceID
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func writeAccepted(ctx context.Context, w http.ResponseWriter, data any) {
	traceID := applog.TraceIDFromContext(ctx)
	resp := map[string]any{
		"code":    0,
		"message": "accepted",
		"data":    data,
	}
	if traceID != "" {
		resp["trace_id"] = traceID
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(resp)
}
