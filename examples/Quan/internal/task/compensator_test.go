package task

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCompensator_compensateOnce_Success(t *testing.T) {
	repo := &fakeCompRepo{
		taskIDs: []RecoveryCandidate{
			{TaskID: 101, Source: RecoverySourceRetryDue, RecoverAt: time.Now().UTC().Add(-time.Second)},
			{TaskID: 102, Source: RecoverySourceRetryDue, RecoverAt: time.Now().UTC().Add(-time.Second)},
			{TaskID: 103, Source: RecoverySourceRetryDue, RecoverAt: time.Now().UTC().Add(-time.Second)},
		},
	}
	queue := &fakeCompQueue{}
	comp, err := NewCompensator(repo, queue, CompensationConfig{
		Enabled:   true,
		BatchSize: 10,
	})
	if err != nil {
		t.Fatalf("new compensator failed: %v", err)
	}

	if err := comp.compensateOnce(context.Background()); err != nil {
		t.Fatalf("compensate once failed: %v", err)
	}
	if len(queue.scheduled) != 3 {
		t.Fatalf("expected 3 scheduled tasks, got %d", len(queue.scheduled))
	}
}

func TestCompensator_compensateOnce_ListFailedError(t *testing.T) {
	repo := &fakeCompRepo{
		listErr: errors.New("injected list error"),
	}
	queue := &fakeCompQueue{}
	comp, err := NewCompensator(repo, queue, CompensationConfig{Enabled: true})
	if err != nil {
		t.Fatalf("new compensator failed: %v", err)
	}

	if err := comp.compensateOnce(context.Background()); err == nil {
		t.Fatalf("expected compensateOnce error when list failed")
	}
}

func TestCompensator_compensateOnce_PartialQueueFailure(t *testing.T) {
	repo := &fakeCompRepo{
		taskIDs: []RecoveryCandidate{
			{TaskID: 201, Source: RecoverySourceRetryDue, RecoverAt: time.Now().UTC().Add(-time.Second)},
			{TaskID: 202, Source: RecoverySourceRetryDue, RecoverAt: time.Now().UTC().Add(-time.Second)},
			{TaskID: 203, Source: RecoverySourceRetryDue, RecoverAt: time.Now().UTC().Add(-time.Second)},
		},
	}
	queue := &fakeCompQueue{
		failTaskID: 202,
	}
	comp, err := NewCompensator(repo, queue, CompensationConfig{Enabled: true})
	if err != nil {
		t.Fatalf("new compensator failed: %v", err)
	}

	if err := comp.compensateOnce(context.Background()); err != nil {
		t.Fatalf("compensateOnce should continue on partial queue errors: %v", err)
	}
	if len(queue.scheduled) != 2 {
		t.Fatalf("expected 2 successful schedules, got %d", len(queue.scheduled))
	}
}

type fakeCompRepo struct {
	taskIDs         []RecoveryCandidate
	suspendedIDs    []RecoveryCandidate
	staleRunningIDs []RecoveryCandidate
	listErr         error
	recoverErr      error
	recovered       []int64
	recoverable     map[int64]bool
}

func (f *fakeCompRepo) ListDueFailedForCompensation(_ context.Context, _ int) ([]RecoveryCandidate, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]RecoveryCandidate(nil), f.taskIDs...), nil
}

func (f *fakeCompRepo) ListSuspendedForCompensation(_ context.Context, _ time.Time, _ int) ([]RecoveryCandidate, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]RecoveryCandidate(nil), f.suspendedIDs...), nil
}

func (f *fakeCompRepo) ListStaleRunningForCompensation(_ context.Context, _ time.Time, _ int) ([]RecoveryCandidate, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]RecoveryCandidate(nil), f.staleRunningIDs...), nil
}

func (f *fakeCompRepo) MarkRecoveredForRetry(_ context.Context, taskID int64, _ int64, _ string) (bool, error) {
	if f.recoverErr != nil {
		return false, f.recoverErr
	}
	f.recovered = append(f.recovered, taskID)
	if f.recoverable != nil {
		return f.recoverable[taskID], nil
	}
	return true, nil
}

type fakeCompQueue struct {
	failTaskID int64
	scheduled  []int64
}

func (f *fakeCompQueue) ScheduleRetry(_ context.Context, taskID int64, _ time.Time) error {
	if f.failTaskID > 0 && taskID == f.failTaskID {
		return errors.New("injected schedule error")
	}
	f.scheduled = append(f.scheduled, taskID)
	return nil
}

func TestCompensator_compensateOnce_RecoversSuspendedAndStaleRunning(t *testing.T) {
	repo := &fakeCompRepo{
		suspendedIDs: []RecoveryCandidate{
			{TaskID: 301, Source: RecoverySourceSuspended, RecoverAt: time.Now().UTC().Add(-2 * time.Second)},
		},
		staleRunningIDs: []RecoveryCandidate{
			{TaskID: 302, Source: RecoverySourceStaleRunning, RecoverAt: time.Now().UTC().Add(-3 * time.Second)},
		},
	}
	queue := &fakeCompQueue{}
	comp, err := NewCompensator(repo, queue, CompensationConfig{Enabled: true})
	if err != nil {
		t.Fatalf("new compensator failed: %v", err)
	}

	if err := comp.compensateOnce(context.Background()); err != nil {
		t.Fatalf("compensate once failed: %v", err)
	}
	if len(repo.recovered) != 2 {
		t.Fatalf("expected 2 recovered tasks, got %d", len(repo.recovered))
	}
	if len(queue.scheduled) != 2 {
		t.Fatalf("expected 2 scheduled recovered tasks, got %d", len(queue.scheduled))
	}
}

func TestCompensator_compensateOnce_SkipsVersionConflictedRecovery(t *testing.T) {
	repo := &fakeCompRepo{
		suspendedIDs: []RecoveryCandidate{
			{TaskID: 401, Version: 9, Source: RecoverySourceSuspended, RecoverAt: time.Now().UTC().Add(-2 * time.Second)},
		},
	}
	queue := &fakeCompQueue{}
	repo.recovered = nil
	repo.recoverable = map[int64]bool{401: false}

	comp, err := NewCompensator(repo, queue, CompensationConfig{Enabled: true})
	if err != nil {
		t.Fatalf("new compensator failed: %v", err)
	}
	if err := comp.compensateOnce(context.Background()); err != nil {
		t.Fatalf("compensate once failed: %v", err)
	}
	if len(queue.scheduled) != 0 {
		t.Fatalf("expected no schedules after optimistic-lock miss, got %d", len(queue.scheduled))
	}
}
