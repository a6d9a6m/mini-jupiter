# Redis Hot-Path Adjudication

## Goal

Move the coupon-claim hot path from "MySQL transaction as first adjudicator"
to "Redis atomic adjudication plus MySQL ledger persistence".

## What Changed

- Redis now performs the first decision for claim admission.
- The decision path is atomic inside a Lua script.
- MySQL still persists the claim, task, and outbox state as the auditable
  result.

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
3. If admitted, MySQL persists the claim and async side effects.
4. Redis finalizes the idempotency result to `SUCCESS:<claim_id>`.
5. If MySQL persistence fails, Redis rolls back the stock and user-count
   reservation.

## Current Boundaries

- Redis campaign state is hydrated lazily from MySQL on first miss.
- The current phase does not yet add a separate stale-reservation reconciler.
- Redis remains the hot-path adjudicator; MySQL remains the auditable ledger.

## Validation

Code-level validation:

```powershell
go test ./examples/Quan/internal/coupon -run "Test(Repository_|Service_)" -v
go test ./...
```

New service-level checks:

- concurrent replay with the same idempotency key reuses the same claim result
- different idempotency keys for the same user still conflict
- sold-out traffic still returns explicit conflict

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
