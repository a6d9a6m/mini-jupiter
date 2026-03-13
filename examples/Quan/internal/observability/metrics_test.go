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

	m.SetOutboxPending(5)
	if got := testutil.ToFloat64(m.outboxPendingGauge); got != 5 {
		t.Fatalf("expected outbox pending gauge 5, got %v", got)
	}

	m.IncTaskRetry()
	m.IncTaskRetry()
	if got := testutil.ToFloat64(m.taskRetryTotal); got != 2 {
		t.Fatalf("expected retry total 2, got %v", got)
	}

	m.IncTaskDLQ()
	if got := testutil.ToFloat64(m.taskDLQTotal); got != 1 {
		t.Fatalf("expected dlq total 1, got %v", got)
	}

	m.IncConsumeSuccess()
	m.IncConsumeSuccess()
	m.IncConsumeFailure()
	if got := testutil.ToFloat64(m.taskConsumeTotal.WithLabelValues("success")); got != 2 {
		t.Fatalf("expected consume success 2, got %v", got)
	}
	if got := testutil.ToFloat64(m.taskConsumeTotal.WithLabelValues("failed")); got != 1 {
		t.Fatalf("expected consume failed 1, got %v", got)
	}
	if got := testutil.ToFloat64(m.taskFailRateGauge); got < 0.333 || got > 0.334 {
		t.Fatalf("expected fail rate around 1/3, got %v", got)
	}

	m.ObserveCouponClaim("business_conflict", "already_claimed", 120*time.Millisecond)
	if got := testutil.ToFloat64(m.couponClaimTotal.WithLabelValues("business_conflict", "already_claimed")); got != 1 {
		t.Fatalf("expected coupon claim total 1, got %v", got)
	}

	m.ObserveTaskRecovery("suspended", 2*time.Second)
	if got := testutil.ToFloat64(m.taskRecoveryTotal.WithLabelValues("suspended")); got != 1 {
		t.Fatalf("expected task recovery total 1, got %v", got)
	}

	m.ObserveAppError(409)
	if got := testutil.ToFloat64(m.appErrorTotal.WithLabelValues("business_conflict", "conflict")); got != 1 {
		t.Fatalf("expected app conflict total 1, got %v", got)
	}
}
