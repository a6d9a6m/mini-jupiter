package metric

import (
	"net/http"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Config struct {
	Enabled   bool   `mapstructure:"enabled" yaml:"enabled"`
	Path      string `mapstructure:"path" yaml:"path"`
	Namespace string `mapstructure:"namespace" yaml:"namespace"`
}

type Metrics struct {
	reqCount   *prometheus.CounterVec
	reqLatency *prometheus.HistogramVec
	inFlight   *prometheus.GaugeVec
	errCount   *prometheus.CounterVec
}

func New(cfg Config) *Metrics {
	ns := cfg.Namespace
	if ns == "" {
		ns = "mini_jupiter"
	}
	m := &Metrics{
		reqCount: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: ns,
				Name:      "http_requests_total",
				Help:      "Total number of HTTP requests.",
			},
			[]string{"method", "path", "status"},
		),
		reqLatency: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: ns,
				Name:      "http_request_duration_seconds",
				Help:      "HTTP request duration in seconds.",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"method", "path", "status"},
		),
		inFlight: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: ns,
				Name:      "http_inflight_requests",
				Help:      "Current number of in-flight HTTP requests.",
			},
			[]string{"method", "path"},
		),
		errCount: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: ns,
				Name:      "http_error_total",
				Help:      "Total number of error responses by error code.",
			},
			[]string{"code"},
		),
	}
	prometheus.MustRegister(m.reqCount, m.reqLatency, m.inFlight, m.errCount)
	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.Handler()
}

func (m *Metrics) Observe(method, path string, status int, seconds float64) {
	if m == nil {
		return
	}
	labels := prometheus.Labels{
		"method": method,
		"path":   path,
		"status": strconv.Itoa(status),
	}
	m.reqCount.With(labels).Inc()
	m.reqLatency.With(labels).Observe(seconds)
}

func (m *Metrics) IncInFlight(method, path string) {
	if m == nil {
		return
	}
	m.inFlight.With(prometheus.Labels{
		"method": method,
		"path":   path,
	}).Inc()
}

func (m *Metrics) DecInFlight(method, path string) {
	if m == nil {
		return
	}
	m.inFlight.With(prometheus.Labels{
		"method": method,
		"path":   path,
	}).Dec()
}

func (m *Metrics) ObserveError(code int) {
	if m == nil {
		return
	}
	m.errCount.With(prometheus.Labels{
		"code": strconv.Itoa(code),
	}).Inc()
}
