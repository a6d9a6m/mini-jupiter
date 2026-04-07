package observability

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMetricsRecorders(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New("quan_test", reg, reg)

	m.ObserveClaimRequestAccept("success", "accepted", 120*time.Millisecond)
	if got := testutil.ToFloat64(m.claimRequestAcceptTotal.WithLabelValues("success", "accepted")); got != 1 {
		t.Fatalf("expected accept total 1, got %v", got)
	}

	m.ObserveClaimRequestPublish("published", 30*time.Millisecond)
	if got := testutil.ToFloat64(m.claimRequestPublishTotal.WithLabelValues("published")); got != 1 {
		t.Fatalf("expected publish total 1, got %v", got)
	}

	m.IncClaimRequestState("ENQUEUED")
	m.IncClaimRequestState("SUCCEEDED")
	if got := testutil.ToFloat64(m.claimRequestStateTotal.WithLabelValues("ENQUEUED")); got != 1 {
		t.Fatalf("expected ENQUEUED transition total 1, got %v", got)
	}
	if got := testutil.ToFloat64(m.claimRequestStateTotal.WithLabelValues("SUCCEEDED")); got != 1 {
		t.Fatalf("expected SUCCEEDED transition total 1, got %v", got)
	}

	m.ObserveClaimRequestConsume("succeeded", 80*time.Millisecond)
	m.ObserveClaimRequestConsume("retryable_persist_error", 60*time.Millisecond)
	if got := testutil.ToFloat64(m.claimRequestConsumeTotal.WithLabelValues("succeeded")); got != 1 {
		t.Fatalf("expected consume succeeded total 1, got %v", got)
	}
	if got := testutil.ToFloat64(m.claimRequestConsumeTotal.WithLabelValues("retryable_persist_error")); got != 1 {
		t.Fatalf("expected consume retryable error total 1, got %v", got)
	}
	if got := testutil.ToFloat64(m.claimRequestConsumeFail); got < 0.49 || got > 0.51 {
		t.Fatalf("expected consume fail rate around 0.5, got %v", got)
	}

	m.ObserveClaimRequestReconcile("processing_finalize", "succeeded", 2*time.Second)
	if got := testutil.ToFloat64(m.claimRequestReconcileTotal.WithLabelValues("processing_finalize", "succeeded")); got != 1 {
		t.Fatalf("expected reconcile total 1, got %v", got)
	}

	m.ObserveAppError(409)
	if got := testutil.ToFloat64(m.appErrorTotal.WithLabelValues("business_conflict", "conflict")); got != 1 {
		t.Fatalf("expected app conflict total 1, got %v", got)
	}
}
