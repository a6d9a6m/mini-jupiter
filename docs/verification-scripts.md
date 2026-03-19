# Verification Scripts

Five script entry points cover the evidence the Quan project needs most.

Run them serially, not in parallel, because they share the same local MySQL,
Redis, and Quan HTTP port.

## 0. Capacity Sweep

Script:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\quan-run-capacity.ps1 `
  -ConcurrencyLevels 100,200,400,800 `
  -RequestsPerStep 20000 `
  -DurationSeconds 10 `
  -StopOnFirstFailure
```

Purpose:

- find the first failing concurrency level on the steady-success claim path
- measure throughput and latency with a dedicated load tool instead of the
  scenario benchmark helper
- prove each step still closes to a consistent ledger

Outputs:

- per-step `vegeta` binary result
- per-step `vegeta` JSON report
- per-step status breakdown and ledger audit
- summary JSON with `max_passed_concurrency` and `first_failing_concurrency`

## 1. High-Conflict Benchmark

Script:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\quan-run-high-conflict.ps1 `
  -Requests 100000 `
  -Concurrency 60
```

Purpose:

- validate whether the claim path holds under high-conflict traffic
- measure QPS and latency under stock bottleneck pressure
- verify no oversell and no per-user overflow after the run

Outputs:

- benchmark report JSON
- ledger audit JSON
- combined summary JSON

Representative heavy sample:

- `100000` requests
- `60` concurrency
- stock pressure fixed at the configured campaign stock

## 2. Skewed-Conflict Benchmark

Script:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\quan-run-skewed-conflict.ps1 `
  -TotalRequests 10000 `
  -TierSpec "2x2500,10x200,3000x1"
```

Purpose:

- validate correctness when request demand is heavily concentrated on a small
  user subset
- measure latency and transport behavior under a non-uniform user distribution
- verify no oversell, no per-user overflow, and no success-vs-ledger drift

Outputs:

- `vegeta` binary result
- `vegeta` JSON report
- ledger audit JSON
- combined summary JSON with per-tier request distribution

## 3. Ledger Consistency Audit

Script:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\quan-audit-ledger.ps1 -CouponId <coupon_id>
```

Purpose:

- prove the result is not only an HTTP success surface
- verify stock, claims, per-user limit, and benchmark success counts converge to
  the same ledger state

Checks:

- oversell count
- per-user overflow count
- available stock delta
- benchmark success count vs persisted claim count

## 4. Fault Injection And Recovery

Script:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\quan-run-fault-recovery.ps1 `
  -RepeatCount 50
```

Purpose:

- validate outbox publish-side recovery
- validate consumer-side suspended recovery
- validate stale running recovery
- validate DLQ replay and duplicate-delivery boundaries

Coverage:

- relay publish failure
- relay mark-published failure
- consumer mark-success failure
- retry scheduling failure
- duplicate ready delivery
- short Redis outage
- stale running recovery
- DLQ replay

Outputs:

- per-run pass/failure records
- per-scenario aggregate pass rate
- total sample count and average recovery time

Boundary:

- the fault-recovery script covers relay, consumer, compensator, and DLQ
  recovery paths
- side-effect dispatcher and reservation reconciler recovery are validated by
  focused coupon package tests rather than this script
