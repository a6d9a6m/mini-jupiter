package taskhandler

import (
	"context"
	"encoding/json"
	"fmt"

	taskmodel "mini-jupiter/examples/Quan/internal/task/model"
	applog "mini-jupiter/pkg/log"

	"go.uber.org/zap"
)

type TaskHandler interface {
	Handle(ctx context.Context, task taskmodel.AsyncTask) error
}

type Registry struct {
	handlers map[string]TaskHandler
}

func NewRegistry() *Registry {
	return &Registry{handlers: map[string]TaskHandler{}}
}

func (r *Registry) Register(taskType string, h TaskHandler) {
	r.handlers[taskType] = h
}

func (r *Registry) Handle(ctx context.Context, task taskmodel.AsyncTask) error {
	h, ok := r.handlers[task.TaskType]
	if !ok {
		return fmt.Errorf("task handler not found: %s", task.TaskType)
	}
	return h.Handle(ctx, task)
}

type consumeReceiptStore interface {
	TryCreate(ctx context.Context, task taskmodel.AsyncTask) (bool, error)
}

type noopConsumeReceiptStore struct{}

func (noopConsumeReceiptStore) TryCreate(context.Context, taskmodel.AsyncTask) (bool, error) {
	return true, nil
}

type SendCouponNoticeHandler struct {
	receipts consumeReceiptStore
}

func NewSendCouponNoticeHandler(receipts consumeReceiptStore) *SendCouponNoticeHandler {
	if receipts == nil {
		receipts = noopConsumeReceiptStore{}
	}
	return &SendCouponNoticeHandler{receipts: receipts}
}

func (h *SendCouponNoticeHandler) Handle(ctx context.Context, task taskmodel.AsyncTask) error {
	var payload taskmodel.SendCouponNoticePayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("decode SEND_COUPON_NOTICE payload: %w", err)
	}
	if payload.TraceID != "" {
		ctx = applog.WithTraceID(ctx, payload.TraceID)
	}
	created, err := h.receipts.TryCreate(ctx, task)
	if err != nil {
		return fmt.Errorf("record SEND_COUPON_NOTICE consume receipt: %w", err)
	}
	if !created {
		applog.L(ctx).Info("coupon notice task deduplicated",
			zap.Int64("task_id", task.ID),
			zap.String("biz_id", task.BizID),
		)
		return nil
	}
	applog.L(ctx).Info("coupon notice task consumed",
		zap.Int64("task_id", task.ID),
		zap.Int64("claim_id", payload.ClaimID),
		zap.Int64("coupon_id", payload.CouponID),
		zap.Int64("user_id", payload.UserID),
	)
	return nil
}
