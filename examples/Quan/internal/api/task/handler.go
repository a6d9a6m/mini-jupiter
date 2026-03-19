package taskapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"mini-jupiter/examples/Quan/internal/task"
	apperr "mini-jupiter/pkg/errors"
	applog "mini-jupiter/pkg/log"
)

const (
	headerUserID           = "X-User-ID"
	headerIdempotencyKey   = "Idempotency-Key"
	timeLayoutRFC3339Milli = "2006-01-02T15:04:05.000Z07:00"
)

type taskService interface {
	CreateTask(ctx context.Context, req task.CreateTaskRequest) (task.AsyncTask, error)
	GetTask(ctx context.Context, taskID int64) (task.AsyncTask, error)
	ReplayDLQTask(ctx context.Context, taskID int64) error
}

type Handler struct {
	svc taskService
}

func NewHandler(svc taskService) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/tasks", h.createTask)
	mux.HandleFunc("GET /api/v1/tasks/{task_id}", h.getTask)
	mux.HandleFunc("POST /api/v1/tasks/{task_id}/replay", h.replayTask)
}

type createTaskRequest struct {
	TaskType string          `json:"task_type"`
	BizID    string          `json:"biz_id"`
	Payload  json.RawMessage `json:"payload"`
	MaxRetry int             `json:"max_retry"`
}

func (h *Handler) createTask(w http.ResponseWriter, r *http.Request) {
	if _, ok := parsePositiveInt64(strings.TrimSpace(r.Header.Get(headerUserID))); !ok {
		apperr.WriteHTTPWithContext(r.Context(), w, apperr.New(apperr.CodeBadRequest, "invalid X-User-ID"))
		return
	}
	if strings.TrimSpace(r.Header.Get(headerIdempotencyKey)) == "" {
		apperr.WriteHTTPWithContext(r.Context(), w, apperr.New(apperr.CodeBadRequest, "missing Idempotency-Key"))
		return
	}
	var req createTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apperr.WriteHTTPWithContext(r.Context(), w, apperr.New(apperr.CodeBadRequest, "invalid request body"))
		return
	}

	taskRec, err := h.svc.CreateTask(r.Context(), task.CreateTaskRequest{
		TaskType: req.TaskType,
		BizID:    req.BizID,
		Payload:  req.Payload,
		MaxRetry: req.MaxRetry,
	})
	if err != nil {
		apperr.WriteHTTPWithContext(r.Context(), w, err)
		return
	}
	writeOK(r.Context(), w, map[string]any{
		"task_id":     taskRec.ID,
		"task_type":   taskRec.TaskType,
		"biz_id":      taskRec.BizID,
		"status":      taskRec.Status,
		"retry_count": taskRec.RetryCount,
	})
}

func (h *Handler) getTask(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parsePositiveInt64(r.PathValue("task_id"))
	if !ok {
		apperr.WriteHTTPWithContext(r.Context(), w, apperr.New(apperr.CodeBadRequest, "invalid task_id"))
		return
	}
	taskRec, err := h.svc.GetTask(r.Context(), taskID)
	if err != nil {
		apperr.WriteHTTPWithContext(r.Context(), w, err)
		return
	}
	resp := map[string]any{
		"task_id":     taskRec.ID,
		"task_type":   taskRec.TaskType,
		"biz_id":      taskRec.BizID,
		"status":      taskRec.Status,
		"retry_count": taskRec.RetryCount,
		"last_error":  taskRec.LastError,
	}
	if taskRec.NextRetry != nil {
		resp["next_retry_at"] = taskRec.NextRetry.UTC().Format(timeLayoutRFC3339Milli)
	}
	writeOK(r.Context(), w, resp)
}

func (h *Handler) replayTask(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parsePositiveInt64(r.PathValue("task_id"))
	if !ok {
		apperr.WriteHTTPWithContext(r.Context(), w, apperr.New(apperr.CodeBadRequest, "invalid task_id"))
		return
	}
	if err := h.svc.ReplayDLQTask(r.Context(), taskID); err != nil {
		apperr.WriteHTTPWithContext(r.Context(), w, err)
		return
	}
	writeOK(r.Context(), w, map[string]any{
		"task_id":  taskID,
		"replayed": true,
	})
}

func parsePositiveInt64(raw string) (int64, bool) {
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
