# mini-jupiter

`mini-jupiter` is a Go backend practice project built around reusable runtime
components and a hardened Quan coupon-claim demo.

The project now focuses on five system-level claims:

- MySQL is the auditable ledger for claim correctness.
- Redis provides the coupon-claim hot-path adjudication.
- Claim side-effects are durably staged before task/outbox dispatch.
- Outbox and compensator turn async gray failures into recoverable state.
- Reliability and performance claims are backed by tests, fault injection, and
  scenario-based benchmark evidence.

## Main Areas

- `examples/Quan`
  Reliability-focused coupon-claim service used as the primary validation
  scenario.
- `pkg/`
  Reusable runtime building blocks such as config, logging, errors, metrics,
  MySQL, Redis, and lifecycle management.
- `internal/middleware`
  Request middleware for trace ID, recovery, and logging.
- `docs/`
  Correctness, async reliability, benchmark, and observability write-ups.

## Quan Quick Start

Start infrastructure:

```powershell
docker compose up -d mysql redis prometheus grafana
```

Run Quan:

```powershell
$env:CONFIG_PATH="examples/Quan/config.sample.yaml"
go run ./examples/Quan
```

Useful endpoints:

- `GET /ping`
- `POST /api/v1/coupons/{coupon_id}/claim`
- `GET /api/v1/coupons/{coupon_id}/claims/me`
- `GET /metrics`

## Test Commands

Targeted reliability checks:

```powershell
go test ./examples/Quan/internal/coupon -run "Test(Repository_|Service_|Adjudicator_|ReservationReconciler_|SideEffectDispatcher_)" -v
go test ./examples/Quan/internal/task -run "Test(E2E_|ConsumeTask|Compensator_)" -v
go test ./examples/Quan/internal/outbox -run TestRelay -v
```

Repository-wide test sweep:

```powershell
go test ./...
```

This includes longer-running task E2E coverage. Use the focused package commands
above first when isolating async regressions.

## Benchmark Lifecycle Scripts

Two PowerShell scripts are provided to avoid stale Quan benchmark processes:

- `scripts/quan-stop.ps1`
  Stops a benchmark-owned Quan process by PID file and can optionally clear the
  owner of `:8081`.
- `scripts/quan-run-bench.ps1`
  Cleans up old Quan processes, starts the app, waits for `/ping`, runs
  `benchprep`, runs `benchclaim`, and always tears the app down in `finally`.

Purpose-specific verification scripts:

- `scripts/quan-run-high-conflict.ps1`
  Runs the high-conflict benchmark and then audits the ledger for oversell,
  per-user overflow, and benchmark-vs-ledger consistency.
- `scripts/quan-run-skewed-conflict.ps1`
  Uses Dockerized `vegeta` to apply a skewed per-user request distribution and
  then audits the ledger for oversell, per-user overflow, and
  success-vs-ledger consistency.
- `scripts/quan-run-capacity.ps1`
  Uses Dockerized `vegeta` to run a steady-success concurrency sweep against the
  real claim endpoint and finds the first failing concurrency level.
- `scripts/quan-audit-ledger.ps1`
  Audits a coupon campaign directly from MySQL and optionally cross-checks a
  benchmark report.
- `scripts/quan-run-fault-recovery.ps1`
  Executes the deterministic fault-injection recovery suite, supports repeated
  runs per scenario, and writes a JSON summary.

Example:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\quan-run-bench.ps1 `
  -Scenario idempotent_replay `
  -CouponId 9601 `
  -Stock 800 `
  -PerUserLimit 1 `
  -Requests 4000 `
  -Concurrency 20 `
  -UserMode cycle `
  -UserPool 800 `
  -StartUserId 930000 `
  -IdemMode per_user `
  -IdemPrefix replay_check
```

Capacity sweep example:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\quan-run-capacity.ps1 `
  -ConcurrencyLevels 100,200,400,800 `
  -RequestsPerStep 20000 `
  -DurationSeconds 10 `
  -StopOnFirstFailure
```

Heavy high-conflict example:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\quan-run-high-conflict.ps1 `
  -Requests 100000 `
  -Concurrency 60
```

Repeated fault-recovery example:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\quan-run-fault-recovery.ps1 `
  -RepeatCount 50
```

Skewed-conflict example:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\quan-run-skewed-conflict.ps1 `
  -TotalRequests 10000 `
  -TierSpec "2x2500,10x200,3000x1"
```

`scripts/quan-run-bench.ps1` expects local MySQL/Redis to already be reachable.
The higher-level wrappers `quan-run-high-conflict.ps1`,
`quan-run-skewed-conflict.ps1`, `quan-run-capacity.ps1`, and
`quan-run-fault-recovery.ps1` can optionally try
`docker compose up -d mysql redis` first via `-StartDockerInfra`.

Run the high-conflict benchmark and fault-recovery scripts serially. They share
the same local MySQL, Redis, and `:8081` service port.
The skewed-conflict and capacity sweep scripts also use the same local service
port and should run serially.

## Key Docs

- [Correctness Model](docs/correctness-model.md)
- [Async Reliability State Machine](docs/async-reliability-state-machine.md)
- [Fault Injection Matrix](docs/fault-injection-matrix.md)
- [Observability](docs/observability.md)
- [Benchmark Methodology](docs/benchmark-methodology.md)
- [Verification Scripts](docs/verification-scripts.md)
- [Redis Hot-Path Adjudication](docs/redis-hotpath-adjudication.md)

## Current Boundary Notes

- The Redis hot path is now the first adjudicator for coupon claims.
- MySQL still records the final claim fact, side-effect obligation, task, and
  outbox state.
- Reservation leases are reconciled in the background to close the crash window
  between Redis admission and MySQL persistence/finalize.
- Claim transactions now persist `claim_side_effects`; task and outbox creation
  are dispatched asynchronously after claim commit.
- Benchmark numbers are scenario-specific local measurements, not production
  capacity claims.
