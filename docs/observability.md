# Observability and SLO Layer

## Scope

Phase 4 adds the minimum observability layer required to answer:

- Is the claim path healthy?
- Are business conflicts separated from server faults?
- Is async backlog growing?
- Are retries, DLQ, and recovery behavior visible?
- Can one request be traced into async aftermath?

## Trace Correlation

The project still uses lightweight trace correlation rather than full
OpenTelemetry spans.

Current correlation path:

- request entry gets `X-Trace-Id`
- synchronous logs include `trace_id`
- coupon claim writes the same `trace_id` into async task payload
- async handler restores `trace_id` into its logger context

This is enough to follow one request from HTTP entry to async consume outcome in
the current stack, without pretending full distributed tracing is already in
place.

## Metrics Naming Contract

HTTP:

- `mini_jupiter_quan_http_requests_total{method,path,status,status_class}`
- `mini_jupiter_quan_http_request_duration_seconds{method,path,status,status_class}`
- `mini_jupiter_quan_http_inflight_requests{method,path}`

Business:

- `mini_jupiter_quan_coupon_claim_total{result_class,result_code}`
- `mini_jupiter_quan_coupon_claim_duration_seconds{result_class}`
- `mini_jupiter_quan_app_error_total{class,code}`

Async:

- `mini_jupiter_quan_outbox_pending`
- `mini_jupiter_quan_task_retry_total`
- `mini_jupiter_quan_task_dlq_total`
- `mini_jupiter_quan_task_consume_total{status}`
- `mini_jupiter_quan_task_consume_fail_rate`
- `mini_jupiter_quan_task_recovery_total{source}`
- `mini_jupiter_quan_task_recovery_latency_seconds{source}`

## Classification Rules

HTTP:

- `2xx` is success
- `4xx` is client-visible non-server failure
- `409` is tracked separately as business conflict
- `5xx` is server fault

Coupon claim results:

- `success`
- `business_conflict/already_claimed`
- `business_conflict/limit_reached`
- `business_conflict/sold_out`
- `client_error/bad_request`
- `server_error/internal_error`

Application errors:

- `business_conflict`
- `client_error`
- `server_error`

This keeps sold-out and already-claimed traffic from being read as server
instability.

## Dashboard Intent

The Grafana dashboard is alert-oriented rather than only descriptive:

- claim success rate
- claim latency P95/P99
- claim conflict vs server fault rate
- HTTP 409 vs 5xx split
- outbox backlog
- task retry rate
- DLQ rate
- recovery latency P95/P99
- recovery throughput by source

## Repeatable Commands

Start dependencies:

```powershell
docker compose up -d
```

Run the Quan server:

```powershell
go run ./examples/Quan
```

Run core recovery and correctness tests:

```powershell
go test ./examples/Quan/internal/coupon -run TestRepository_ -v
go test ./examples/Quan/internal/outbox -run TestRelay -v
go test ./examples/Quan/internal/task -run TestE2E_ -v
```

## Boundary

Phase 4 supports:

- visible separation of `409` and `5xx`
- request-to-async trace correlation with propagated `trace_id`
- minimal SLO and alert-oriented dashboard coverage

Phase 4 does not claim:

- full distributed tracing spans
- production-complete alert routing
- complete operational runbooks
