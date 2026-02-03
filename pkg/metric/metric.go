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
	}
	prometheus.MustRegister(m.reqCount, m.reqLatency)
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
