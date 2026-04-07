package main

type runParams struct {
	Scenario     string `json:"scenario"`
	BaseURL      string `json:"base_url"`
	CouponID     int64  `json:"coupon_id"`
	Requests     int    `json:"requests"`
	Concurrency  int    `json:"concurrency"`
	TimeoutMs    int64  `json:"timeout_ms"`
	UserMode     string `json:"user_mode"`
	UserPool     int    `json:"user_pool"`
	StartUserID  int64  `json:"start_user_id"`
	IdemMode     string `json:"idem_mode"`
	IdemPrefix   string `json:"idem_prefix"`
	FixedIdemKey string `json:"fixed_idem_key"`
}

type businessStats struct {
	Success     int     `json:"success"`
	Total       int     `json:"total"`
	SuccessRate float64 `json:"success_rate"`
}

type latencyStats struct {
	Min float64 `json:"min"`
	Avg float64 `json:"avg"`
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

type runResult struct {
	Scenario         string         `json:"scenario"`
	StartedAt        string         `json:"started_at"`
	FinishedAt       string         `json:"finished_at"`
	DurationSeconds  float64        `json:"duration_seconds"`
	QPS              float64        `json:"qps"`
	Params           runParams      `json:"params"`
	LatencyMS        latencyStats   `json:"latency_ms"`
	HTTPStatusCounts map[string]int `json:"http_status_counts"`
	AppCodeCounts    map[string]int `json:"app_code_counts"`
	TransportErrors  map[string]int `json:"transport_errors"`
	Business         businessStats  `json:"business"`
}

type singleResult struct {
	latencyNs int64
	httpCode  int
	appCode   int
	errText   string
}

type responseEnvelope struct {
	Code int `json:"code"`
}
