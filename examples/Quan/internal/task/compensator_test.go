package task

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCompensator_compensateOnce_Success(t *testing.T) {
	repo := &fakeCompRepo{
		taskIDs: []int64{101, 102, 103},
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
		taskIDs: []int64{201, 202, 203},
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
	taskIDs []int64
	listErr error
}

func (f *fakeCompRepo) ListDueFailedForCompensation(_ context.Context, _ int) ([]int64, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]int64(nil), f.taskIDs...), nil
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
