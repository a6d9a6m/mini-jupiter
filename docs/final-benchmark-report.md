# Final Benchmark Report

## Date

2026-03-13

## Environment

- host: local Windows workstation
- app: `go run ./examples/Quan` using `examples/Quan/config.sample.yaml`
- endpoint: `http://127.0.0.1:8081`
- MySQL: Docker Compose `mysql:8.4`
- Redis: Docker Compose `redis:7-alpine`

## Scenario Results

| Scenario | Concurrency | Requests | Result Summary | QPS | P95 ms | P99 ms | Success | Conflict | Transport Errors |
| --- | ---: | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| baseline | 40 | 4000 | no stock pressure, all unique users | 54.06 | 822.68 | 852.52 | 4000 | 0 | 0 |
| high_conflict | 60 | 4000 | stock bottleneck with explicit business conflict | 393.92 | 999.58 | 1237.82 | 500 | 3500 | 0 |
| idempotent_replay | 20 | 4000 | replay-heavy repeated users with per-user idem key | 245.34 | 391.73 | 436.91 | 800 | 3200 | 0 |

Source files:

- `examples/Quan/doc/report/phase5_baseline.json`
- `examples/Quan/doc/report/phase5_high_conflict.json`
- `examples/Quan/doc/report/phase5_idempotent_replay.json`

## Reading the Results

### `baseline`

- This is the cleanest steady-success scenario in the current project.
- It shows stable all-`200` behavior with no transport errors.
- Throughput is modest because the current correctness-first path still pays
  MySQL transaction and async bookkeeping cost on every successful claim.

### `high_conflict`

- This scenario demonstrates the intended separation between business conflict
  and server failure.
- The system sold exactly `500` claims and returned `3500` explicit `409`
  conflicts, with no transport errors in the accepted run.
- This is stronger evidence than a raw QPS number because it shows bounded
  correctness under conflict pressure.

### `idempotent_replay`

- This scenario did not collapse into server errors or transport failures.
- However, replay traffic did not mostly return reused successful results.
- Instead, the final run produced `800` successful responses and `3200` `409`
  conflicts.

Inference:

- The project currently preserves correctness under replay-heavy pressure, but
  does not yet deliver a strong replay-hit ratio under this traffic shape.
- That is a boundary worth stating explicitly rather than smoothing over.

## Attribution

What improved by the hardening phases:

- conflict-heavy traffic now surfaces primarily as `409`, not silent failure
- async recovery paths are explicit and observable
- benchmark scenarios are cleanly reset and scenario-stable

What cost was introduced:

- successful baseline throughput is still limited by the correctness-first write
  path
- conflict and replay-heavy traffic still spend real work budget on admission
  and conflict handling

## Boundary

These benchmark results support:

- scenario-based performance claims
- explicit conflict handling claims
- replay boundary claims with measured evidence

They do not support:

- production capacity claims
- cross-machine scaling claims
- "the system is fast" as a context-free statement
