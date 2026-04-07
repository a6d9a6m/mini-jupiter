package request

import (
	"context"
	"errors"
	"testing"
	"time"
)

var (
	errTestPublishUnavailable = errors.New("mq unavailable")
	errTestSQLUnavailable     = errors.New("sql unavailable")
	errTestLookupFailed       = errors.New("lookup failed")
)

func TestAcceptService_RecordsStateMetrics(t *testing.T) {
	t.Run("accepted_to_enqueued", func(t *testing.T) {
		store := newFakeRequestStore()
		pub := &fakePublisher{}
		hotpath := &fakeHotPath{
			decision: Decision{Code: DecisionCodeAdmitted, RequestID: "req-metric-accept"},
		}
		metrics := &acceptMetricsRecorder{}
		svc := NewAcceptService(hotpath, store, pub)
		svc.SetMetrics(metrics)

		_, err := svc.Accept(context.Background(), AcceptRequest{
			CouponID:       1301,
			UserID:         2301,
			IdempotencyKey: "idem-metric-accept",
		})
		if err != nil {
			t.Fatalf("accept failed: %v", err)
		}
		assertStringSlice(t, metrics.states, []string{string(StatusAccepted), string(StatusEnqueued)})
	})

	t.Run("publish_failure_marks_publishing", func(t *testing.T) {
		store := newFakeRequestStore()
		pub := &fakePublisher{err: errTestPublishUnavailable}
		hotpath := &fakeHotPath{
			decision: Decision{Code: DecisionCodeAdmitted, RequestID: "req-metric-publishing"},
		}
		metrics := &acceptMetricsRecorder{}
		svc := NewAcceptService(hotpath, store, pub)
		svc.SetMetrics(metrics)

		resp, err := svc.Accept(context.Background(), AcceptRequest{
			CouponID:       1302,
			UserID:         2302,
			IdempotencyKey: "idem-metric-publishing",
		})
		if err != nil {
			t.Fatalf("accept should return recoverable response: %v", err)
		}
		if resp.Status != StatusPublishing {
			t.Fatalf("expected publishing response, got %s", resp.Status)
		}
		assertStringSlice(t, metrics.states, []string{string(StatusAccepted), string(StatusPublishing)})
	})
}

func TestConsumer_RecordsConsumeAndStateMetrics(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		store := newFakeRequestStore()
		if err := store.Create(context.Background(), Request{
			ID:             "req-metric-consume-success",
			CouponID:       1303,
			UserID:         2303,
			IdempotencyKey: "idem-metric-consume-success",
			Status:         StatusEnqueued,
		}); err != nil {
			t.Fatalf("seed request failed: %v", err)
		}
		metrics := &consumeMetricsRecorder{}
		consumer := NewConsumer(store, &fakeClaimWriter{claimID: 9303, inserted: true}, &fakeHotPath{})
		consumer.SetMetrics(metrics)

		if err := consumer.ConsumeAccepted(context.Background(), "req-metric-consume-success"); err != nil {
			t.Fatalf("consume failed: %v", err)
		}
		assertStringSlice(t, metrics.states, []string{string(StatusProcessing), string(StatusSucceeded)})
		if len(metrics.results) != 1 || metrics.results[0] != "succeeded" {
			t.Fatalf("expected consume result succeeded, got %+v", metrics.results)
		}
		if metrics.durations[0] < 0 {
			t.Fatalf("expected non-negative duration, got %v", metrics.durations[0])
		}
	})

	t.Run("rolled_back", func(t *testing.T) {
		store := newFakeRequestStore()
		if err := store.Create(context.Background(), Request{
			ID:             "req-metric-consume-rollback",
			CouponID:       1304,
			UserID:         2304,
			IdempotencyKey: "idem-metric-consume-rollback",
			Status:         StatusEnqueued,
		}); err != nil {
			t.Fatalf("seed request failed: %v", err)
		}
		metrics := &consumeMetricsRecorder{}
		consumer := NewConsumer(store, &fakeClaimWriter{err: errTestSQLUnavailable}, &fakeHotPath{})
		consumer.SetMetrics(metrics)

		if err := consumer.ConsumeAccepted(context.Background(), "req-metric-consume-rollback"); err != nil {
			t.Fatalf("consume should converge rolled back: %v", err)
		}
		assertStringSlice(t, metrics.states, []string{string(StatusProcessing), string(StatusRolledBack)})
		if len(metrics.results) != 1 || metrics.results[0] != "rolled_back" {
			t.Fatalf("expected consume result rolled_back, got %+v", metrics.results)
		}
	})
}

func TestReconciler_RecordsMetrics(t *testing.T) {
	stale := time.Now().UTC().Add(-time.Minute)
	t.Run("accepted_republish", func(t *testing.T) {
		store := newFakeRequestStore()
		if err := store.Create(context.Background(), Request{
			ID:             "req-metric-reconcile-enqueue",
			CouponID:       1305,
			UserID:         2305,
			IdempotencyKey: "idem-metric-reconcile-enqueue",
			Status:         StatusAccepted,
			AcceptedAt:     stale,
			UpdatedAt:      stale,
		}); err != nil {
			t.Fatalf("seed request failed: %v", err)
		}
		pub := &fakePublisher{}
		metrics := &reconcileMetricsRecorder{}
		reconciler := NewReconciler(store, pub, &fakeHotPath{}, &fakeClaimLookup{}, ReconcilePolicy{
			PublishStaleAfter:    time.Second,
			ProcessingStaleAfter: time.Second,
		})
		reconciler.SetMetrics(metrics)

		if err := reconciler.ReconcileOnce(context.Background(), 10); err != nil {
			t.Fatalf("reconcile failed: %v", err)
		}
		assertStringSlice(t, metrics.states, []string{string(StatusEnqueued)})
		if len(metrics.actions) != 1 || metrics.actions[0] != "accepted_republish:succeeded" {
			t.Fatalf("unexpected reconcile actions: %+v", metrics.actions)
		}
	})

	t.Run("processing_lookup_error", func(t *testing.T) {
		store := newFakeRequestStore()
		if err := store.Create(context.Background(), Request{
			ID:             "req-metric-reconcile-lookup-error",
			CouponID:       1306,
			UserID:         2306,
			IdempotencyKey: "idem-metric-reconcile-lookup-error",
			Status:         StatusProcessing,
			AcceptedAt:     stale,
			UpdatedAt:      stale,
		}); err != nil {
			t.Fatalf("seed request failed: %v", err)
		}
		metrics := &reconcileMetricsRecorder{}
		reconciler := NewReconciler(store, &fakePublisher{}, &fakeHotPath{}, errorClaimLookup{err: errTestLookupFailed}, ReconcilePolicy{
			PublishStaleAfter:    time.Second,
			ProcessingStaleAfter: time.Second,
		})
		reconciler.SetMetrics(metrics)

		err := reconciler.ReconcileOnce(context.Background(), 10)
		if err == nil {
			t.Fatal("expected reconcile lookup error")
		}
		if len(metrics.actions) != 1 || metrics.actions[0] != "processing_lookup:lookup_error" {
			t.Fatalf("unexpected reconcile actions: %+v", metrics.actions)
		}
		if len(metrics.states) != 0 {
			t.Fatalf("expected no state increments on lookup error, got %+v", metrics.states)
		}
	})
}

type acceptMetricsRecorder struct {
	states []string
}

func (r *acceptMetricsRecorder) IncClaimRequestState(status string) {
	r.states = append(r.states, status)
}

type consumeMetricsRecorder struct {
	results   []string
	durations []time.Duration
	states    []string
}

func (r *consumeMetricsRecorder) ObserveClaimRequestConsume(result string, duration time.Duration) {
	r.results = append(r.results, result)
	r.durations = append(r.durations, duration)
}

func (r *consumeMetricsRecorder) IncClaimRequestState(status string) {
	r.states = append(r.states, status)
}

type reconcileMetricsRecorder struct {
	actions   []string
	durations []time.Duration
	states    []string
}

func (r *reconcileMetricsRecorder) ObserveClaimRequestReconcile(action, result string, duration time.Duration) {
	r.actions = append(r.actions, action+":"+result)
	r.durations = append(r.durations, duration)
}

func (r *reconcileMetricsRecorder) IncClaimRequestState(status string) {
	r.states = append(r.states, status)
}

func assertStringSlice(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("unexpected slice length: got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected slice content: got=%v want=%v", got, want)
		}
	}
}
