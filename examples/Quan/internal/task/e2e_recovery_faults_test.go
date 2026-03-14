package task

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"mini-jupiter/examples/Quan/internal/outbox"
)

type injectedQueueConfig struct {
	publishReadyFailures  int32
	publishReadyErr       error
	scheduleRetryFailures int32
	scheduleRetryErr      error
}

type injectedQueue struct {
	inner              *Queue
	publishReadyFault  *deterministicFault
	scheduleRetryFault *deterministicFault
}

func newInjectedQueue(inner *Queue, cfg injectedQueueConfig) *injectedQueue {
	return &injectedQueue{
		inner:              inner,
		publishReadyFault:  newDeterministicFault(cfg.publishReadyFailures, cfg.publishReadyErr),
		scheduleRetryFault: newDeterministicFault(cfg.scheduleRetryFailures, cfg.scheduleRetryErr),
	}
}

func (q *injectedQueue) PublishReady(ctx context.Context, taskID int64) error {
	if err := q.publishReadyFault.Fail(); err != nil {
		return err
	}
	return q.inner.PublishReady(ctx, taskID)
}

func (q *injectedQueue) PushDLQ(ctx context.Context, taskID int64) error {
	return q.inner.PushDLQ(ctx, taskID)
}

func (q *injectedQueue) ReplayFromDLQ(ctx context.Context, taskID int64) (bool, error) {
	return q.inner.ReplayFromDLQ(ctx, taskID)
}

func (q *injectedQueue) MoveDueRetryToReady(ctx context.Context, batch int) (int, error) {
	return q.inner.MoveDueRetryToReady(ctx, batch)
}

func (q *injectedQueue) PopReady(ctx context.Context, timeout time.Duration) (int64, bool, error) {
	return q.inner.PopReady(ctx, timeout)
}

func (q *injectedQueue) ScheduleRetry(ctx context.Context, taskID int64, retryAt time.Time) error {
	if err := q.scheduleRetryFault.Fail(); err != nil {
		return err
	}
	return q.inner.ScheduleRetry(ctx, taskID, retryAt)
}

type injectedRelayRepo struct {
	inner              *outbox.Repository
	markPublishedFault *deterministicFault
}

func newInjectedRelayRepo(inner *outbox.Repository, failures int32, err error) *injectedRelayRepo {
	return &injectedRelayRepo{
		inner:              inner,
		markPublishedFault: newDeterministicFault(failures, err),
	}
}

func (r *injectedRelayRepo) ListDispatchable(ctx context.Context, limit int) ([]outbox.Event, error) {
	return r.inner.ListDispatchable(ctx, limit)
}

func (r *injectedRelayRepo) CountPending(ctx context.Context) (int64, error) {
	return r.inner.CountPending(ctx)
}

func (r *injectedRelayRepo) TryMarkDispatching(ctx context.Context, eventID int64) (bool, error) {
	return r.inner.TryMarkDispatching(ctx, eventID)
}

func (r *injectedRelayRepo) MarkPublished(ctx context.Context, eventID int64) error {
	if err := r.markPublishedFault.Fail(); err != nil {
		return err
	}
	return r.inner.MarkPublished(ctx, eventID)
}

func (r *injectedRelayRepo) MarkRetry(ctx context.Context, eventID int64, delay time.Duration, lastErr string) error {
	return r.inner.MarkRetry(ctx, eventID, delay, lastErr)
}

func (r *injectedRelayRepo) MarkSuspended(ctx context.Context, eventID int64, lastErr string) error {
	return r.inner.MarkSuspended(ctx, eventID, lastErr)
}

func (r *injectedRelayRepo) RecoverStaleDispatching(ctx context.Context, staleBefore time.Time, limit int) (int64, error) {
	return r.inner.RecoverStaleDispatching(ctx, staleBefore, limit)
}

type injectedTaskRepo struct {
	inner            *Repository
	markSuccessFault *deterministicFault
}

func newInjectedTaskRepo(inner *Repository, failures int32, err error) *injectedTaskRepo {
	return &injectedTaskRepo{
		inner:            inner,
		markSuccessFault: newDeterministicFault(failures, err),
	}
}

func (r *injectedTaskRepo) TryMarkRunning(ctx context.Context, taskID int64) (AsyncTask, bool, error) {
	return r.inner.TryMarkRunning(ctx, taskID)
}

func (r *injectedTaskRepo) MarkFailed(ctx context.Context, taskID int64, expectedVersion int64, lastErr string, backoffBase time.Duration) (bool, *time.Time, error) {
	return r.inner.MarkFailed(ctx, taskID, expectedVersion, lastErr, backoffBase)
}

func (r *injectedTaskRepo) MarkSuccess(ctx context.Context, taskID int64, expectedVersion int64) error {
	if err := r.markSuccessFault.Fail(); err != nil {
		return err
	}
	return r.inner.MarkSuccess(ctx, taskID, expectedVersion)
}

func (r *injectedTaskRepo) MarkSuspended(ctx context.Context, taskID int64, expectedVersion int64, lastErr string) error {
	return r.inner.MarkSuspended(ctx, taskID, expectedVersion, lastErr)
}

func (r *injectedTaskRepo) ListDueFailedForCompensation(ctx context.Context, limit int) ([]RecoveryCandidate, error) {
	return r.inner.ListDueFailedForCompensation(ctx, limit)
}

func (r *injectedTaskRepo) ListSuspendedForCompensation(ctx context.Context, staleBefore time.Time, limit int) ([]RecoveryCandidate, error) {
	return r.inner.ListSuspendedForCompensation(ctx, staleBefore, limit)
}

func (r *injectedTaskRepo) ListStaleRunningForCompensation(ctx context.Context, staleBefore time.Time, limit int) ([]RecoveryCandidate, error) {
	return r.inner.ListStaleRunningForCompensation(ctx, staleBefore, limit)
}

func (r *injectedTaskRepo) MarkRecoveredForRetry(ctx context.Context, taskID int64, expectedVersion int64, lastErr string) (bool, error) {
	return r.inner.MarkRecoveredForRetry(ctx, taskID, expectedVersion, lastErr)
}

type deterministicFault struct {
	remaining atomic.Int32
	fired     atomic.Int32
	err       error
}

func newDeterministicFault(times int32, err error) *deterministicFault {
	f := &deterministicFault{err: err}
	f.remaining.Store(times)
	return f
}

func (f *deterministicFault) Fail() error {
	if f == nil {
		return nil
	}
	for {
		remain := f.remaining.Load()
		if remain <= 0 {
			return nil
		}
		if f.remaining.CompareAndSwap(remain, remain-1) {
			f.fired.Add(1)
			if f.err != nil {
				return f.err
			}
			return errors.New("injected deterministic fault")
		}
	}
}

func (f *deterministicFault) FiredCount() int32 {
	if f == nil {
		return 0
	}
	return f.fired.Load()
}
