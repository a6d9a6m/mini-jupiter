package coupon

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	apperr "mini-jupiter/pkg/errors"
	applog "mini-jupiter/pkg/log"
)

const (
	headerUserID         = "X-User-ID"
	headerIdempotencyKey = "Idempotency-Key"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/coupons/{coupon_id}/claim", h.claimCoupon)
	mux.HandleFunc("GET /api/v1/coupons/{coupon_id}/claims/me", h.getMyClaim)
}

func (h *Handler) claimCoupon(w http.ResponseWriter, r *http.Request) {
	userID, ok := parseUserID(r)
	if !ok {
		apperr.WriteHTTPWithContext(r.Context(), w, apperr.New(apperr.CodeBadRequest, "invalid X-User-ID"))
		return
	}
	couponID, ok := parsePathInt64(r.PathValue("coupon_id"))
	if !ok {
		apperr.WriteHTTPWithContext(r.Context(), w, apperr.New(apperr.CodeBadRequest, "invalid coupon_id"))
		return
	}
	idemKey := strings.TrimSpace(r.Header.Get(headerIdempotencyKey))
	if idemKey == "" {
		apperr.WriteHTTPWithContext(r.Context(), w, apperr.New(apperr.CodeBadRequest, "missing Idempotency-Key"))
		return
	}

	rec, err := h.svc.Claim(r.Context(), couponID, userID, idemKey)
	if err != nil {
		apperr.WriteHTTPWithContext(r.Context(), w, err)
		return
	}

	writeOK(r.Context(), w, map[string]any{
		"claim_id":   rec.ID,
		"coupon_id":  rec.CouponID,
		"user_id":    rec.UserID,
		"status":     rec.Status,
		"claimed_at": rec.CreatedAt.Format(time.RFC3339),
	})
}

func (h *Handler) getMyClaim(w http.ResponseWriter, r *http.Request) {
	userID, ok := parseUserID(r)
	if !ok {
		apperr.WriteHTTPWithContext(r.Context(), w, apperr.New(apperr.CodeBadRequest, "invalid X-User-ID"))
		return
	}
	couponID, ok := parsePathInt64(r.PathValue("coupon_id"))
	if !ok {
		apperr.WriteHTTPWithContext(r.Context(), w, apperr.New(apperr.CodeBadRequest, "invalid coupon_id"))
		return
	}
	rec, err := h.svc.GetMyClaim(r.Context(), couponID, userID)
	if err != nil {
		apperr.WriteHTTPWithContext(r.Context(), w, err)
		return
	}
	writeOK(r.Context(), w, map[string]any{
		"claimed":   true,
		"claim_id":  rec.ID,
		"coupon_id": rec.CouponID,
		"user_id":   rec.UserID,
		"status":    rec.Status,
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
