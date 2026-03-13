package task

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

type flakyTaskHandler struct {
	failTimes int
	mu        sync.Mutex
	attempts  map[int64]int
}

func (h *flakyTaskHandler) Handle(_ context.Context, task AsyncTask) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.attempts == nil {
		h.attempts = map[int64]int{}
	}
	h.attempts[task.ID]++
	if h.attempts[task.ID] <= h.failTimes {
		return errors.New("injected handler failure")
	}
	return nil
}

func (h *flakyTaskHandler) Attempts(taskID int64) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.attempts[taskID]
}

type toggleTaskHandler struct {
	fail atomic.Bool
}

func (h *toggleTaskHandler) Handle(_ context.Context, _ AsyncTask) error {
	if h.fail.Load() {
		return errors.New("injected always fail")
	}
	return nil
}

func (h *toggleTaskHandler) SetFail(v bool) {
	h.fail.Store(v)
}

type scheduleFailureQueue struct {
	inner           *Queue
	remainFailCalls atomic.Int32
	failedCalls     atomic.Int32
}

func newScheduleFailureQueue(inner *Queue, failCalls int32) *scheduleFailureQueue {
	q := &scheduleFailureQueue{inner: inner}
	q.remainFailCalls.Store(failCalls)
	return q
}

func (q *scheduleFailureQueue) PublishReady(ctx context.Context, taskID int64) error {
	return q.inner.PublishReady(ctx, taskID)
}

func (q *scheduleFailureQueue) PushDLQ(ctx context.Context, taskID int64) error {
	return q.inner.PushDLQ(ctx, taskID)
}

func (q *scheduleFailureQueue) ReplayFromDLQ(ctx context.Context, taskID int64) (bool, error) {
	return q.inner.ReplayFromDLQ(ctx, taskID)
}

func (q *scheduleFailureQueue) MoveDueRetryToReady(ctx context.Context, batch int) (int, error) {
	return q.inner.MoveDueRetryToReady(ctx, batch)
}

func (q *scheduleFailureQueue) PopReady(ctx context.Context, timeout time.Duration) (int64, bool, error) {
	return q.inner.PopReady(ctx, timeout)
}

func (q *scheduleFailureQueue) ScheduleRetry(ctx context.Context, taskID int64, retryAt time.Time) error {
	for {
		remain := q.remainFailCalls.Load()
		if remain <= 0 {
			break
		}
		if q.remainFailCalls.CompareAndSwap(remain, remain-1) {
			q.failedCalls.Add(1)
			return errors.New("injected schedule retry failure")
		}
	}
	return q.inner.ScheduleRetry(ctx, taskID, retryAt)
}

func (q *scheduleFailureQueue) FailedScheduleCalls() int32 {
	return q.failedCalls.Load()
}
