package claim

import (
	"context"
	"errors"
	"testing"
	"time"

	"mini-jupiter/examples/Quan/internal/outbox"
	"mini-jupiter/examples/Quan/internal/task"
)

type fakeSideEffectDispatchRepo struct {
	items          []ClaimSideEffect
	tryErrIDs      map[int64]bool
	markProcessing []int64
	markDone       []int64
	markRetryIDs   []int64
	markSuspendIDs []int64
	markRetryErr   error
	markSuspendErr error
}

func (f *fakeSideEffectDispatchRepo) RecoverStaleProcessing(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func (f *fakeSideEffectDispatchRepo) ListDispatchable(context.Context, int) ([]ClaimSideEffect, error) {
	cp := make([]ClaimSideEffect, len(f.items))
	copy(cp, f.items)
	return cp, nil
}

func (f *fakeSideEffectDispatchRepo) TryMarkProcessing(_ context.Context, sideEffectID int64) (bool, error) {
	f.markProcessing = append(f.markProcessing, sideEffectID)
	if f.tryErrIDs != nil && f.tryErrIDs[sideEffectID] {
		return false, errors.New("mark processing failed")
	}
	return true, nil
}

func (f *fakeSideEffectDispatchRepo) MarkSuspended(_ context.Context, sideEffectID int64, _ string) error {
	f.markSuspendIDs = append(f.markSuspendIDs, sideEffectID)
	return f.markSuspendErr
}
func (f *fakeSideEffectDispatchRepo) MarkRetry(_ context.Context, sideEffectID int64, _ time.Duration, _ string) error {
	f.markRetryIDs = append(f.markRetryIDs, sideEffectID)
	return f.markRetryErr
}
func (f *fakeSideEffectDispatchRepo) MarkDone(_ context.Context, sideEffectID, _, _ int64) error {
	f.markDone = append(f.markDone, sideEffectID)
	return nil
}

type fakeSideEffectTaskRepo struct{}

func (fakeSideEffectTaskRepo) Create(_ context.Context, p task.CreateTaskParams) (task.AsyncTask, error) {
	return task.AsyncTask{ID: 7000 + int64(len(p.BizID)), TaskType: p.TaskType, BizID: p.BizID}, nil
}

func (fakeSideEffectTaskRepo) GetByTypeBiz(_ context.Context, taskType, bizID string) (task.AsyncTask, error) {
	return task.AsyncTask{ID: 1, TaskType: taskType, BizID: bizID}, nil
}

type fakeSideEffectOutboxRepo struct{}

func (fakeSideEffectOutboxRepo) FindByAggregate(context.Context, string, string, string) (outbox.Event, bool, error) {
	return outbox.Event{}, false, nil
}

func (fakeSideEffectOutboxRepo) Create(_ context.Context, p outbox.CreateEventParams) (outbox.Event, error) {
	return outbox.Event{ID: 9001, EventType: p.EventType, AggregateType: p.AggregateType, AggregateID: p.AggregateID, PayloadJSON: p.PayloadJSON}, nil
}

func TestSideEffectDispatcher_TryMarkProcessingFailureContinuesBatch(t *testing.T) {
	payload1, err := MarshalClaimSideEffectPayload(ClaimSideEffectPayload{ClaimID: 1001, CouponID: 2001, UserID: 3001})
	if err != nil {
		t.Fatalf("marshal payload1 failed: %v", err)
	}
	payload2, err := MarshalClaimSideEffectPayload(ClaimSideEffectPayload{ClaimID: 1002, CouponID: 2002, UserID: 3002})
	if err != nil {
		t.Fatalf("marshal payload2 failed: %v", err)
	}
	repo := &fakeSideEffectDispatchRepo{
		items: []ClaimSideEffect{
			{ID: 21, ClaimID: 1001, PayloadJSON: payload1},
			{ID: 22, ClaimID: 1002, PayloadJSON: payload2},
		},
		tryErrIDs: map[int64]bool{21: true},
	}
	dispatcher, err := NewSideEffectDispatcher(repo, fakeSideEffectTaskRepo{}, fakeSideEffectOutboxRepo{}, SideEffectDispatchConfig{Enabled: true})
	if err != nil {
		t.Fatalf("new side effect dispatcher failed: %v", err)
	}

	if err := dispatcher.recoverAndDispatchOnce(context.Background()); err != nil {
		t.Fatalf("recover and dispatch once failed: %v", err)
	}
	if len(repo.markProcessing) != 2 {
		t.Fatalf("expected both side effects to attempt mark processing, got %+v", repo.markProcessing)
	}
	if len(repo.markDone) != 1 || repo.markDone[0] != 22 {
		t.Fatalf("expected second side effect to still complete, got %+v", repo.markDone)
	}
}

func TestSideEffectDispatcher_MarkRetryFailureContinuesBatch(t *testing.T) {
	repo := &fakeSideEffectDispatchRepo{
		items: []ClaimSideEffect{
			{ID: 31, ClaimID: 1001, PayloadJSON: []byte(`"bad"`), RetryCount: 0},
			{ID: 32, ClaimID: 1002, PayloadJSON: mustMarshalSideEffectPayload(t, ClaimSideEffectPayload{ClaimID: 1002, CouponID: 2002, UserID: 3002})},
		},
		markRetryErr: errors.New("retry state write failed"),
	}
	dispatcher, err := NewSideEffectDispatcher(repo, fakeSideEffectTaskRepo{}, fakeSideEffectOutboxRepo{}, SideEffectDispatchConfig{Enabled: true, MaxRetry: 3})
	if err != nil {
		t.Fatalf("new side effect dispatcher failed: %v", err)
	}

	if err := dispatcher.recoverAndDispatchOnce(context.Background()); err != nil {
		t.Fatalf("recover and dispatch once failed: %v", err)
	}
	if len(repo.markRetryIDs) != 1 || repo.markRetryIDs[0] != 31 {
		t.Fatalf("expected first side effect to attempt retry mark, got %+v", repo.markRetryIDs)
	}
	if len(repo.markDone) != 1 || repo.markDone[0] != 32 {
		t.Fatalf("expected second side effect to still complete, got %+v", repo.markDone)
	}
}

func TestSideEffectDispatcher_MarkSuspendedFailureContinuesBatch(t *testing.T) {
	repo := &fakeSideEffectDispatchRepo{
		items: []ClaimSideEffect{
			{ID: 41, ClaimID: 1001, PayloadJSON: []byte(`"bad"`), RetryCount: 0},
			{ID: 42, ClaimID: 1002, PayloadJSON: mustMarshalSideEffectPayload(t, ClaimSideEffectPayload{ClaimID: 1002, CouponID: 2002, UserID: 3002})},
		},
		markSuspendErr: errors.New("suspend state write failed"),
	}
	dispatcher, err := NewSideEffectDispatcher(repo, fakeSideEffectTaskRepo{}, fakeSideEffectOutboxRepo{}, SideEffectDispatchConfig{Enabled: true, MaxRetry: 1})
	if err != nil {
		t.Fatalf("new side effect dispatcher failed: %v", err)
	}

	if err := dispatcher.recoverAndDispatchOnce(context.Background()); err != nil {
		t.Fatalf("recover and dispatch once failed: %v", err)
	}
	if len(repo.markSuspendIDs) != 1 || repo.markSuspendIDs[0] != 41 {
		t.Fatalf("expected first side effect to attempt suspend mark, got %+v", repo.markSuspendIDs)
	}
	if len(repo.markDone) != 1 || repo.markDone[0] != 42 {
		t.Fatalf("expected second side effect to still complete, got %+v", repo.markDone)
	}
}

func mustMarshalSideEffectPayload(t *testing.T, payload ClaimSideEffectPayload) []byte {
	t.Helper()
	raw, err := MarshalClaimSideEffectPayload(payload)
	if err != nil {
		t.Fatalf("marshal side effect payload failed: %v", err)
	}
	return raw
}
