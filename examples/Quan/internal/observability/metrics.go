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
	claimRequestAcceptTotal    *prometheus.CounterVec
	claimRequestAcceptLatency  *prometheus.HistogramVec
	claimRequestPublishTotal   *prometheus.CounterVec
	claimRequestPublishLatency *prometheus.HistogramVec
	claimRequestConsumeTotal   *prometheus.CounterVec
	claimRequestConsumeLatency *prometheus.HistogramVec
	claimRequestConsumeFail    prometheus.Gauge
	claimRequestStateTotal     *prometheus.CounterVec
	claimRequestReconcileTotal *prometheus.CounterVec
	claimRequestReconcileLat   *prometheus.HistogramVec
	appErrorTotal              *prometheus.CounterVec

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
		claimRequestAcceptTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "claim_request_accept_total",
			Help:      "Total number of claim request accept responses by normalized outcome.",
		}, []string{"result_class", "result_code"}),
		claimRequestAcceptLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "claim_request_accept_duration_seconds",
			Help:      "Claim request accept latency in seconds.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"result_class"}),
		claimRequestPublishTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "claim_request_publish_total",
			Help:      "Total number of request publish attempts by result.",
		}, []string{"result"}),
		claimRequestPublishLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "claim_request_publish_duration_seconds",
			Help:      "Request publish latency in seconds.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"result"}),
		claimRequestConsumeTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "claim_request_consume_total",
			Help:      "Total number of request consumer outcomes by result.",
		}, []string{"result"}),
		claimRequestConsumeLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "claim_request_consume_duration_seconds",
			Help:      "Request consumer handling latency in seconds.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"result"}),
		claimRequestConsumeFail: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "claim_request_consume_fail_rate",
			Help:      "Claim request consume fail ratio (fail/total).",
		}),
		claimRequestStateTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "claim_request_state_transition_total",
			Help:      "Total number of request state transitions by status.",
		}, []string{"status"}),
		claimRequestReconcileTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "claim_request_reconcile_total",
			Help:      "Total number of request reconcile actions by action and result.",
		}, []string{"action", "result"}),
		claimRequestReconcileLat: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "claim_request_reconcile_duration_seconds",
			Help:      "Claim request reconcile action latency in seconds.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"action", "result"}),
		appErrorTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "app_error_total",
			Help:      "Application errors grouped by normalized class and code.",
		}, []string{"class", "code"}),
		gatherer: gatherer,
	}
	reg.MustRegister(
		m.claimRequestAcceptTotal,
		m.claimRequestAcceptLatency,
		m.claimRequestPublishTotal,
		m.claimRequestPublishLatency,
		m.claimRequestConsumeTotal,
		m.claimRequestConsumeLatency,
		m.claimRequestConsumeFail,
		m.claimRequestStateTotal,
		m.claimRequestReconcileTotal,
		m.claimRequestReconcileLat,
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

func (m *Metrics) ObserveClaimRequestAccept(resultClass, resultCode string, duration time.Duration) {
	if m == nil {
		return
	}
	if resultClass == "" {
		resultClass = "unknown"
	}
	if resultCode == "" {
		resultCode = "unknown"
	}
	m.claimRequestAcceptTotal.WithLabelValues(resultClass, resultCode).Inc()
	m.claimRequestAcceptLatency.WithLabelValues(resultClass).Observe(duration.Seconds())
}

func (m *Metrics) ObserveClaimRequestPublish(result string, duration time.Duration) {
	if m == nil {
		return
	}
	if result == "" {
		result = "unknown"
	}
	m.claimRequestPublishTotal.WithLabelValues(result).Inc()
	m.claimRequestPublishLatency.WithLabelValues(result).Observe(duration.Seconds())
}

func (m *Metrics) ObserveClaimRequestConsume(result string, duration time.Duration) {
	if m == nil {
		return
	}
	if result == "" {
		result = "unknown"
	}
	m.claimRequestConsumeTotal.WithLabelValues(result).Inc()
	m.claimRequestConsumeLatency.WithLabelValues(result).Observe(duration.Seconds())
	switch result {
	case "succeeded", "rolled_back", "terminal_noop":
		atomic.AddUint64(&m.consumeSuccess, 1)
	default:
		atomic.AddUint64(&m.consumeFail, 1)
	}
	m.refreshConsumeFailRate()
}

func (m *Metrics) IncClaimRequestState(status string) {
	if m == nil {
		return
	}
	if status == "" {
		status = "unknown"
	}
	m.claimRequestStateTotal.WithLabelValues(status).Inc()
}

func (m *Metrics) ObserveClaimRequestReconcile(action, result string, duration time.Duration) {
	if m == nil {
		return
	}
	if action == "" {
		action = "unknown"
	}
	if result == "" {
		result = "unknown"
	}
	m.claimRequestReconcileTotal.WithLabelValues(action, result).Inc()
	m.claimRequestReconcileLat.WithLabelValues(action, result).Observe(duration.Seconds())
}

func (m *Metrics) ObserveAppError(code int) {
	if m == nil || code == apperr.CodeOK {
		return
	}
	m.appErrorTotal.WithLabelValues(classifyAppError(code), classifyAppErrorCode(code)).Inc()
}

func (m *Metrics) refreshConsumeFailRate() {
	fail := atomic.LoadUint64(&m.consumeFail)
	success := atomic.LoadUint64(&m.consumeSuccess)
	total := fail + success
	if total == 0 {
		m.claimRequestConsumeFail.Set(0)
		return
	}
	m.claimRequestConsumeFail.Set(float64(fail) / float64(total))
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
