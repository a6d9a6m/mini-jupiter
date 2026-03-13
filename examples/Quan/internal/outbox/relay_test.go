package outbox

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeRelayRepo struct {
	events  []Event
	listErr error

	tryMarkDispatchingErr bool
	markPublishedErr      error
	markRetryErr          error
	markSuspendedErr      error
	recoverErr            error

	mu               sync.Mutex
	listCalls        int
	tryDispatchIDs   []int64
	markPublishedIDs []int64
	markRetryIDs     []int64
	markSuspendedIDs []int64
	recoverCalls     int
}

func (f *fakeRelayRepo) ListDispatchable(_ context.Context, _ int) ([]Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	cp := make([]Event, len(f.events))
	copy(cp, f.events)
	return cp, nil
}

func (f *fakeRelayRepo) CountPending(_ context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return int64(len(f.events)), nil
}

func (f *fakeRelayRepo) TryMarkDispatching(_ context.Context, eventID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tryDispatchIDs = append(f.tryDispatchIDs, eventID)
	if f.tryMarkDispatchingErr {
		return false, errors.New("mark dispatching failed")
	}
	return true, nil
}

func (f *fakeRelayRepo) MarkPublished(_ context.Context, eventID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markPublishedIDs = append(f.markPublishedIDs, eventID)
	return f.markPublishedErr
}

func (f *fakeRelayRepo) MarkRetry(_ context.Context, eventID int64, _ time.Duration, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markRetryIDs = append(f.markRetryIDs, eventID)
	return f.markRetryErr
}

func (f *fakeRelayRepo) MarkSuspended(_ context.Context, eventID int64, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markSuspendedIDs = append(f.markSuspendedIDs, eventID)
	return f.markSuspendedErr
}

func (f *fakeRelayRepo) RecoverStaleDispatching(_ context.Context, _ time.Time, _ int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recoverCalls++
	if f.recoverErr != nil {
		return 0, f.recoverErr
	}
	return 0, nil
}

type fakePublisher struct {
	err error

	mu        sync.Mutex
	published []int64
}

func (p *fakePublisher) PublishReady(_ context.Context, taskID int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.published = append(p.published, taskID)
	return p.err
}

func TestRelayDispatchSuccessMarksPublished(t *testing.T) {
	repo := &fakeRelayRepo{
		events: []Event{
			{
				ID:          1,
				PayloadJSON: []byte(`{"task_id":123}`),
				RetryCount:  0,
			},
		},
	}
	pub := &fakePublisher{}
	relay, err := NewRelay(repo, pub, RelayConfig{Enabled: true})
	if err != nil {
		t.Fatalf("new relay failed: %v", err)
	}

	if err := relay.dispatchOnce(context.Background()); err != nil {
		t.Fatalf("dispatch once failed: %v", err)
	}
	if len(pub.published) != 1 || pub.published[0] != 123 {
		t.Fatalf("expected published task 123, got %+v", pub.published)
	}
	if len(repo.tryDispatchIDs) != 1 || repo.tryDispatchIDs[0] != 1 {
		t.Fatalf("expected dispatch attempt for event 1, got %+v", repo.tryDispatchIDs)
	}
	if len(repo.markPublishedIDs) != 1 || repo.markPublishedIDs[0] != 1 {
		t.Fatalf("expected mark published event 1, got %+v", repo.markPublishedIDs)
	}
	if len(repo.markRetryIDs) != 0 || len(repo.markSuspendedIDs) != 0 {
		t.Fatalf("expected no retry or suspend mark, got retry=%+v suspended=%+v", repo.markRetryIDs, repo.markSuspendedIDs)
	}
}

func TestRelayDispatchInvalidPayloadMarksSuspended(t *testing.T) {
	repo := &fakeRelayRepo{
		events: []Event{
			{
				ID:          2,
				PayloadJSON: []byte(`{"task_id":"bad"}`),
				RetryCount:  1,
			},
		},
	}
	pub := &fakePublisher{}
	relay, err := NewRelay(repo, pub, RelayConfig{Enabled: true})
	if err != nil {
		t.Fatalf("new relay failed: %v", err)
	}

	if err := relay.dispatchOnce(context.Background()); err != nil {
		t.Fatalf("dispatch once failed: %v", err)
	}
	if len(pub.published) != 0 {
		t.Fatalf("expected no publish, got %+v", pub.published)
	}
	if len(repo.markRetryIDs) != 0 {
		t.Fatalf("expected no retry mark, got %+v", repo.markRetryIDs)
	}
	if len(repo.markSuspendedIDs) != 1 || repo.markSuspendedIDs[0] != 2 {
		t.Fatalf("expected suspended mark event 2, got %+v", repo.markSuspendedIDs)
	}
}

func TestRelayDispatchPublisherFailureMarksRetry(t *testing.T) {
	repo := &fakeRelayRepo{
		events: []Event{
			{
				ID:          3,
				PayloadJSON: []byte(`{"task_id":456}`),
				RetryCount:  1,
			},
		},
	}
	pub := &fakePublisher{err: errors.New("redis down")}
	relay, err := NewRelay(repo, pub, RelayConfig{Enabled: true})
	if err != nil {
		t.Fatalf("new relay failed: %v", err)
	}

	if err := relay.dispatchOnce(context.Background()); err != nil {
		t.Fatalf("dispatch once failed: %v", err)
	}
	if len(pub.published) != 1 || pub.published[0] != 456 {
		t.Fatalf("expected one publish attempt for task 456, got %+v", pub.published)
	}
	if len(repo.markPublishedIDs) != 0 {
		t.Fatalf("expected no mark published, got %+v", repo.markPublishedIDs)
	}
	if len(repo.markRetryIDs) != 1 || repo.markRetryIDs[0] != 3 {
		t.Fatalf("expected retry mark event 3, got %+v", repo.markRetryIDs)
	}
}

func TestRelayDispatchMarkPublishedFailureLeavesDispatchingForRecovery(t *testing.T) {
	repo := &fakeRelayRepo{
		events: []Event{
			{
				ID:          4,
				PayloadJSON: []byte(`{"task_id":789}`),
				RetryCount:  2,
			},
		},
		markPublishedErr: errors.New("db write failed"),
	}
	pub := &fakePublisher{}
	relay, err := NewRelay(repo, pub, RelayConfig{Enabled: true})
	if err != nil {
		t.Fatalf("new relay failed: %v", err)
	}

	if err := relay.dispatchOnce(context.Background()); err != nil {
		t.Fatalf("dispatch once failed: %v", err)
	}
	if len(pub.published) != 1 || pub.published[0] != 789 {
		t.Fatalf("expected published task 789, got %+v", pub.published)
	}
	if len(repo.markPublishedIDs) != 1 || repo.markPublishedIDs[0] != 4 {
		t.Fatalf("expected mark published event 4, got %+v", repo.markPublishedIDs)
	}
	if len(repo.markRetryIDs) != 0 {
		t.Fatalf("expected no retry mark after publish success, got %+v", repo.markRetryIDs)
	}
}

func TestRelayRunRecoversStaleDispatching(t *testing.T) {
	repo := &fakeRelayRepo{}
	pub := &fakePublisher{}
	relay, err := NewRelay(repo, pub, RelayConfig{
		Enabled:         true,
		PollInterval:    5 * time.Millisecond,
		DispatchTimeout: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new relay failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		relay.run(ctx)
		close(done)
	}()
	time.Sleep(15 * time.Millisecond)
	cancel()
	<-done

	if repo.recoverCalls == 0 {
		t.Fatalf("expected stale dispatching recovery to be called")
	}
}
