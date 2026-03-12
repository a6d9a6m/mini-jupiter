package observability

import (
	"net/http"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	outboxPendingGauge prometheus.Gauge
	taskRetryTotal     prometheus.Counter
	taskDLQTotal       prometheus.Counter
	taskConsumeTotal   *prometheus.CounterVec
	taskFailRateGauge  prometheus.Gauge

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
		gatherer: gatherer,
	}
	reg.MustRegister(
		m.outboxPendingGauge,
		m.taskRetryTotal,
		m.taskDLQTotal,
		m.taskConsumeTotal,
		m.taskFailRateGauge,
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
