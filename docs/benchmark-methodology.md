# Benchmark Methodology

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
