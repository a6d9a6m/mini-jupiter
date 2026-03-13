package observability

import (
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	apperr "mini-jupiter/pkg/errors"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	outboxPendingGauge  prometheus.Gauge
	taskRetryTotal      prometheus.Counter
	taskDLQTotal        prometheus.Counter
	taskConsumeTotal    *prometheus.CounterVec
	taskFailRateGauge   prometheus.Gauge
	couponClaimTotal    *prometheus.CounterVec
	couponClaimLatency  *prometheus.HistogramVec
	taskRecoveryTotal   *prometheus.CounterVec
	taskRecoveryLatency *prometheus.HistogramVec
	appErrorTotal       *prometheus.CounterVec

	consumeSuccess uint64
	consumeFail    uint64
	gatherer       prometheus.Gatherer
}

func New(namespace string, reg prometheus.Registerer, gatherer prometheus.Gatherer) *Metrics {
	if namespace == "" {
		namespace = "mini_jupiter_quan"
	}
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	if gatherer == nil {
		gatherer = prometheus.DefaultGatherer
	}

	m := &Metrics{
		outboxPendingGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "outbox_pending",
			Help:      "Current pending outbox event count.",
		}),
		taskRetryTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "task_retry_total",
			Help:      "Total number of task retries.",
		}),
		taskDLQTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "task_dlq_total",
			Help:      "Total number of tasks moved to DLQ.",
		}),
		taskConsumeTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "task_consume_total",
			Help:      "Total number of task consumption results.",
		}, []string{"status"}),
		taskFailRateGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "task_consume_fail_rate",
			Help:      "Task consume fail ratio (fail/total).",
		}),
		couponClaimTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "coupon_claim_total",
			Help:      "Total number of coupon claim results by normalized outcome.",
		}, []string{"result_class", "result_code"}),
		couponClaimLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "coupon_claim_duration_seconds",
			Help:      "Coupon claim request latency in seconds.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"result_class"}),
		taskRecoveryTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "task_recovery_total",
			Help:      "Total number of task recoveries by source.",
		}, []string{"source"}),
		taskRecoveryLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "task_recovery_latency_seconds",
			Help:      "Latency from task becoming recoverable to compensator rescheduling it.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"source"}),
		appErrorTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "app_error_total",
			Help:      "Application errors grouped by normalized class and code.",
		}, []string{"class", "code"}),
		gatherer: gatherer,
	}
	reg.MustRegister(
		m.outboxPendingGauge,
		m.taskRetryTotal,
		m.taskDLQTotal,
		m.taskConsumeTotal,
		m.taskFailRateGauge,
		m.couponClaimTotal,
		m.couponClaimLatency,
		m.taskRecoveryTotal,
		m.taskRecoveryLatency,
		m.appErrorTotal,
	)
	return m
}

func (m *Metrics) Handler() http.Handler {
	if m == nil {
		return promhttp.Handler()
	}
	return promhttp.HandlerFor(m.gatherer, promhttp.HandlerOpts{})
}

func (m *Metrics) SetOutboxPending(v float64) {
	if m == nil {
		return
	}
	m.outboxPendingGauge.Set(v)
}

func (m *Metrics) IncTaskRetry() {
	if m == nil {
		return
	}
	m.taskRetryTotal.Inc()
}

func (m *Metrics) IncTaskDLQ() {
	if m == nil {
		return
	}
	m.taskDLQTotal.Inc()
}

func (m *Metrics) IncConsumeSuccess() {
	if m == nil {
		return
	}
	m.taskConsumeTotal.WithLabelValues("success").Inc()
	atomic.AddUint64(&m.consumeSuccess, 1)
	m.refreshFailRate()
}

func (m *Metrics) IncConsumeFailure() {
	if m == nil {
		return
	}
	m.taskConsumeTotal.WithLabelValues("failed").Inc()
	atomic.AddUint64(&m.consumeFail, 1)
	m.refreshFailRate()
}

func (m *Metrics) ObserveCouponClaim(resultClass, resultCode string, duration time.Duration) {
	if m == nil {
		return
	}
	if resultClass == "" {
		resultClass = "unknown"
	}
	if resultCode == "" {
		resultCode = "unknown"
	}
	m.couponClaimTotal.WithLabelValues(resultClass, resultCode).Inc()
	m.couponClaimLatency.WithLabelValues(resultClass).Observe(duration.Seconds())
}

func (m *Metrics) ObserveTaskRecovery(source string, latency time.Duration) {
	if m == nil {
		return
	}
	if source == "" {
		source = "unknown"
	}
	m.taskRecoveryTotal.WithLabelValues(source).Inc()
	m.taskRecoveryLatency.WithLabelValues(source).Observe(latency.Seconds())
}

func (m *Metrics) ObserveAppError(code int) {
	if m == nil || code == apperr.CodeOK {
		return
	}
	m.appErrorTotal.WithLabelValues(classifyAppError(code), classifyAppErrorCode(code)).Inc()
}

func (m *Metrics) refreshFailRate() {
	fail := atomic.LoadUint64(&m.consumeFail)
	success := atomic.LoadUint64(&m.consumeSuccess)
	total := fail + success
	if total == 0 {
		m.taskFailRateGauge.Set(0)
		return
	}
	m.taskFailRateGauge.Set(float64(fail) / float64(total))
}

func classifyAppError(code int) string {
	switch code {
	case apperr.CodeConflict:
		return "business_conflict"
	case apperr.CodeBadRequest, apperr.CodeNotFound, apperr.CodeTooManyRequests:
		return "client_error"
	case apperr.CodeInternalError:
		return "server_error"
	default:
		if code >= 500 {
			return "server_error"
		}
		if code >= 400 {
			return "client_error"
		}
		return "unknown"
	}
}

func classifyAppErrorCode(code int) string {
	switch code {
	case apperr.CodeConflict:
		return "conflict"
	case apperr.CodeBadRequest:
		return "bad_request"
	case apperr.CodeNotFound:
		return "not_found"
	case apperr.CodeTooManyRequests:
		return "too_many_requests"
	case apperr.CodeInternalError:
		return "internal_error"
	default:
		return "code_" + strconv.Itoa(code)
	}
}
