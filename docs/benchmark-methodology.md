# Benchmark Methodology

Quan now has two benchmark lanes:

- scenario benchmarks driven by `examples/Quan/bench/cmd/benchclaim`
- capacity scans driven by Dockerized `vegeta`

Use the first lane when you want replay/conflict behavior evidence. Use the
second lane when you want to find the first failing concurrency level on the
steady-success claim path.

## High-Conflict Pressure

Script:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\quan-run-high-conflict.ps1 `
  -Requests 100000 `
  -Concurrency 60
```

Method:

- traffic uses unique users and unique idempotency keys
- stock remains intentionally low relative to total requests, so the scenario
  measures explicit conflict handling under sustained contention
- the run is followed by a MySQL ledger audit to verify no oversell, no
  per-user overflow, and no benchmark-vs-ledger drift

## Capacity Scan

Script:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\quan-run-capacity.ps1 `
  -ConcurrencyLevels 100,200,400,800 `
  -RequestsPerStep 20000 `
  -DurationSeconds 10 `
  -StopOnFirstFailure
```

Method:

- every step provisions a fresh coupon campaign with `stock = unique request pool`
- traffic uses unique users and unique idempotency keys, so every request is
  expected to succeed with `200`
- `vegeta` runs from Docker against the real HTTP endpoint with a fixed worker
  cap equal to the step concurrency, `rate=0`, and a fixed duration
- each step finishes with a ledger audit to verify no oversell, no per-user
  overflow, and no stock drift
- the summary also verifies that observed requests do not exceed the unique
  request pool, so the scan does not silently wrap and replay old targets

Failure rule:

- any non-`200` response
- any transport error
- any ledger audit failure

## Date

2026-03-13

## Goal

Produce repeatable, scenario-based benchmark evidence for the Quan claim path.

## Environment

- host: local Windows workstation
- app process: `go run ./examples/Quan` with `CONFIG_PATH=examples/Quan/config.sample.yaml`
- app endpoint: `http://127.0.0.1:8081`
- MySQL: Docker Compose `mysql:8.4`
- Redis: Docker Compose `redis:7-alpine`
- Prometheus/Grafana were running but not used as traffic generators

## Data Reset Rule

Every scenario must start from a clean campaign prepared with:

```powershell
go run ./examples/Quan/bench/cmd/benchprep `
  -dsn "root:root@tcp(127.0.0.1:3306)/mini_jupiter?parseTime=true&loc=Local&charset=utf8mb4" `
  -coupon-id <coupon_id> -stock <stock> -per-user-limit <limit> -campaign-name <name>
```

Do not reuse a previous scenario's `coupon_id`.

## Scenario Definitions

### `baseline`

- goal: measure steady successful claims without stock bottleneck
- user mode: `unique`
- idempotency mode: `unique`
- expected dominant result: `HTTP 200`

### `high_conflict`

- goal: measure hotspot contention where stock is the bottleneck
- user mode: `unique`
- idempotency mode: `unique`
- expected dominant result: `HTTP 409`

### `idempotent_replay`

- goal: measure replay-heavy traffic with repeated users and per-user idempotency keys
- user mode: `cycle`
- idempotency mode: `per_user`
- expected result: replay reuse should dominate; conflicts indicate a replay boundary or race

### `recovery_after_injected_failure`

- goal: validate recovery path timing after a deterministic publish-side Redis outage
- evidence source: targeted E2E test rather than the HTTP benchmark tool
- command:

```powershell
go test ./examples/Quan/internal/task -run TestE2E_FaultInjection_ShortRedisOutageOnPublishReady_Recovered -count=1 -v
```

## Commands Used

Preferred runner:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\quan-run-bench.ps1 `
  -Scenario <scenario> -CouponId <coupon_id> -Stock <stock> -PerUserLimit <limit> `
  -Requests <requests> -Concurrency <concurrency> -UserMode <user_mode> `
  -UserPool <user_pool> -StartUserId <start_user_id> -IdemMode <idem_mode> `
  -IdemPrefix <idem_prefix>
```

The script handles:

- stale process cleanup on `:8081`
- app startup and `/ping` probe
- `benchprep`
- `benchclaim`
- process teardown in `finally`

### Baseline

```powershell
go run ./examples/Quan/bench/cmd/benchclaim `
  -scenario baseline -base-url http://127.0.0.1:8081 -coupon-id 9201 `
  -requests 4000 -concurrency 40 -user-mode unique -user-pool 4000 -start-user-id 600000 `
  -idem-mode unique -idem-prefix phase5_baseline `
  -report-out examples/Quan/doc/report/phase5_baseline.json
```

### High Conflict

```powershell
go run ./examples/Quan/bench/cmd/benchclaim `
  -scenario high_conflict -base-url http://127.0.0.1:8081 -coupon-id 9202 `
  -requests 4000 -concurrency 60 -user-mode unique -user-pool 4000 -start-user-id 700000 `
  -idem-mode unique -idem-prefix phase5_conflict `
  -report-out examples/Quan/doc/report/phase5_high_conflict.json
```

### Idempotent Replay

```powershell
go run ./examples/Quan/bench/cmd/benchclaim `
  -scenario idempotent_replay -base-url http://127.0.0.1:8081 -coupon-id 9203 `
  -requests 4000 -concurrency 20 -user-mode cycle -user-pool 800 -start-user-id 800000 `
  -idem-mode per_user -idem-prefix phase5_replay `
  -report-out examples/Quan/doc/report/phase5_idempotent_replay.json
```

## Boundary Notes

- These numbers are single-host local results, not capacity limits.
- They are suitable for comparing traffic shapes and identifying bottlenecks.
- They are not suitable for claiming production throughput or internet-scale capacity.
- Always use the lifecycle-managed script instead of leaving a benchmark server
  running in the background.
