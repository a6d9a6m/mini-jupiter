# Resume Wording Draft

## Short Version

- Built a Go service reliability substrate around a high-conflict coupon-claim
  workload, with explicit correctness boundaries, recoverable async delivery,
  deterministic fault injection, and scenario-based benchmark evidence.

## Bullet Version

- Implemented a correctness-first coupon-claim path in Go, with written
  invariants for replay, conflict, sold-out, per-user limit, and auditable
  MySQL commit boundaries.
- Reworked the async pipeline into an explicit state machine using task and
  outbox states, making publish ambiguity, suspended execution, retry, and DLQ
  paths visible and recoverable.
- Added deterministic fault injection and E2E recovery validation for relay
  publish failure, mark-published failure, mark-success failure, duplicate
  delivery, transient Redis outage, stale-running recovery, and DLQ replay.
- Built Prometheus/Grafana observability for claim outcomes, `409` vs `5xx`
  separation, outbox backlog, retry/DLQ flow, and task recovery latency.
- Produced scenario-based benchmark and reliability reports instead of isolated
  QPS numbers, including baseline, high-conflict, idempotent replay, and
  injected-failure recovery evidence.

## Interview Version

- `MySQL` is the auditable committed state for correctness.
- `Outbox` turns send-after-commit ambiguity into recoverable state rather than
  silent failure.
- `Compensator` is not just a retry loop; it closes suspended and stale-running
  async states that normal retries do not close.
- Reliability claims are bounded by explicit failure models and test evidence,
  not by "exactly-once" language.
