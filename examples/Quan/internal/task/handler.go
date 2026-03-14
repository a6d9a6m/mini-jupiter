package task

import (
	"context"
	"encoding/json"
	"fmt"

	applog "mini-jupiter/pkg/log"

	"go.uber.org/zap"
)

type TaskHandler interface {
	// Handle must be idempotent because the delivery contract is at-least-once.
	// A task may be replayed after publish ambiguity, stale RUNNING recovery,
	// suspended-state recovery, or manual DLQ replay.
	Handle(ctx context.Context, task AsyncTask) error
}

type HandlerRegistry struct {
	handlers map[string]TaskHandler
}

func NewHandlerRegistry() *HandlerRegistry {
	return &HandlerRegistry{
		handlers: map[string]TaskHandler{},
	}
}

func (r *HandlerRegistry) Register(taskType string, h TaskHandler) {
	r.handlers[taskType] = h
}

func (r *HandlerRegistry) Handle(ctx context.Context, task AsyncTask) error {
	h, ok := r.handlers[task.TaskType]
	if !ok {
		return fmt.Errorf("task handler not found: %s", task.TaskType)
	}
	return h.Handle(ctx, task)
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

func (h *SendCouponNoticeHandler) Handle(ctx context.Context, task AsyncTask) error {
	var payload SendCouponNoticePayload
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
