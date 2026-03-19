# Redis Hot-Path Adjudication

## Goal

Move the coupon-claim hot path from "MySQL transaction as first adjudicator"
to "Redis atomic adjudication plus MySQL ledger persistence".

## What Changed

- Redis now performs the first decision for claim admission.
- The decision path is atomic inside a Lua script.
- MySQL still persists the claim fact, side-effect obligation, and downstream
  async state as the auditable result.

## Redis Decision Model

Redis stores four kinds of keys per campaign:

- `quan:claim:campaign:<coupon_id>:stock`
  Remaining stock for the hot path.
- `quan:claim:campaign:<coupon_id>:meta`
  Campaign status, active window, and per-user limit.
- `quan:claim:campaign:<coupon_id>:user_count`
  Per-user claim count for fast limit checks.
- `quan:claim:coupon:<coupon_id>:user:<user_id>:idem:<idem_key>`
  Idempotency result state.

Idempotency result values are:

- `PENDING:<reservation_id>`
- `SUCCESS:<claim_id>`

Each admitted reservation also writes a Redis lease record and an index entry:

- `quan:claim:reservation:<reservation_id>`
  Lease metadata, coupon/user/idempotency tuple, and current lease state.
- `quan:claim:reservation:index`
  Sorted-set index keyed by `lease_until_ms` for reconciler scanning.

## Decision Outcomes

The Redis script returns one of:

- `ADMITTED`
- `IDEM_HIT`
- `PENDING`
- `ALREADY_CLAIMED`
- `LIMIT_REACHED`
- `SOLD_OUT`
- `INACTIVE`
- `CAMPAIGN_MISS`

## Execution Flow

1. Service asks Redis for claim adjudication.
2. Redis atomically checks idempotency, user limit, campaign state, and stock.
3. If admitted, MySQL persists the claim fact and a durable
   `claim_side_effects` record.
4. Redis finalizes the idempotency result to `SUCCESS:<claim_id>`.
5. Waiting duplicate callers subscribe to a per-idempotency Redis result
   channel instead of polling Redis in a tight loop.
6. A background side-effect dispatcher converts the durable side-effect record
   into an async task row and an outbox event.
7. If MySQL persistence fails inline, Redis rolls back the stock and user-count
   reservation immediately.
8. If the process crashes after Redis admission but before MySQL persistence or
   Redis finalize completes, a background reservation reconciler scans expired
   leases and either finalizes from the persisted MySQL claim or rolls the
   reservation back.

## Reservation Reconciler

The reconciler closes the crash window between Redis admission and MySQL
persistence/finalize:

- `LEASED` + no MySQL claim: roll back stock, user count, and pending idem key.
- `LEASED` + persisted MySQL claim: finalize the Redis idem key to
  `SUCCESS:<claim_id>`.
- finalized or rolled-back leases are removed from the scan index and kept only
  as short-lived audit state.
- reconcile scans are best-effort per lease; one lookup/finalize/rollback
  failure is logged and later leases in the same batch still continue

## Side-Effect Dispatch

The claim transaction no longer creates async task and outbox rows inline.
Instead it persists one durable side-effect record per claim:

- claim fact remains inside the synchronous correctness boundary
- task/outbox creation moves to a background dispatcher
- dispatcher retries stale or failed processing until the side effect reaches
  `DONE`
- dispatcher batches are also best-effort per item; mark/retry/suspend failures
  are logged and later side effects in the same batch still continue

## Current Boundaries

- Redis campaign state is hydrated lazily from MySQL on first miss.
- `EnsureCampaign` now hydrates campaign stock and meta atomically in one Lua
  script and repairs incomplete hot-path state if only one side exists.
- when that repair path is hit, the service emits a warning so the condition is
  no longer silent
- Lease duration is conservative but static; there is no in-flight lease renew.
- claim success no longer implies task/outbox rows exist inline at commit time;
  it implies a durable side-effect record exists for later dispatch
- Redis remains the hot-path adjudicator; MySQL remains the auditable ledger.

## Validation

Code-level validation:

```powershell
go test ./examples/Quan/internal/coupon -run "Test(Repository_|Service_|Adjudicator_|ReservationReconciler_|SideEffectDispatcher_)" -v
go test ./examples/Quan/internal/task -run TestE2E_FaultInjection_ConsumerMarkSuccessFailure_DeduplicatesSideEffectByUniqueKey -count=1 -v
```

New service-level checks:

- concurrent replay with the same idempotency key reuses the same claim result
- different idempotency keys for the same user still conflict
- sold-out traffic still returns explicit conflict
- partial Redis campaign state is repaired when only stock or meta exists
- waiting duplicate callers wake on finalize publish instead of polling Redis
  in a tight loop
- canceled or degraded waits fall back to `no result yet` instead of surfacing a
  hot-path error
- claim commit creates a pending side-effect record without inline task/outbox
- side-effect dispatcher later creates task/outbox and marks the record done
- replay after success-update ambiguity is deduplicated for the covered handler
  via a unique consume receipt
- expired admitted reservations roll back if no MySQL claim was persisted
- expired admitted reservations finalize if MySQL claim persistence succeeded

Lifecycle-script smoke benchmark:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\quan-run-bench.ps1 `
  -Scenario idempotent_replay_smoke `
  -CouponId 9701 `
  -Stock 40 `
  -PerUserLimit 1 `
  -Requests 200 `
  -Concurrency 5 `
  -UserMode cycle `
  -UserPool 40 `
  -StartUserId 940000 `
  -IdemMode per_user `
  -IdemPrefix smoke_replay
```

Observed result on 2026-03-13:

- `200/200` successful responses
- `0` transport errors
- no leftover listener on `:8081` after script teardown
