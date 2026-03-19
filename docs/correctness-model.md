# Correctness Model

## Scope

This document defines the current correctness boundary for the Quan
coupon-claim path after the Redis hot-path adjudicator and claim side-effect
staging were introduced.

Current implementation status in this phase:

- Redis is the first adjudicator for claim admission and replay
- MySQL-committed claim rows define the final auditable claim result
- `claim_side_effects` is part of the same commit boundary as the claim row
- `async_tasks` and `outbox_events` are created later by a dispatcher, not
  inside the claim transaction

This means correctness is now split into:

- synchronous claim correctness
- asynchronous side-effect obligation correctness

## Formal Design Choice

Correctness is judged by MySQL-committed state:

- `coupon_claims` records the committed claim result
- `claim_side_effects` records the committed obligation to create async follow-up
  work after claim commit

Redis is the first hot-path adjudicator, but Redis contents still do not
override MySQL-committed claim facts.

## Invariants

### Claim Invariants

1. A committed claim is visible only after the transaction commits.
2. For the same `(coupon_id, user_id, idempotency_key)`, request replay returns
   the same committed claim record and does not deduct stock twice.
3. For `per_user_limit = 1`, a second distinct request from the same user is a
   conflict: the system returns `already claimed`.
4. For `per_user_limit > 1`, a user cannot exceed the configured limit in
   committed rows.

### Stock Invariants

1. `coupon_campaigns.available_stock` must never be committed as a negative
   value.
2. The number of committed claim rows for a campaign must not exceed its issued
   stock budget.
3. Stock deduction and claim creation happen in the same database transaction.

### Side-Effect Invariants

1. A newly committed claim must commit its corresponding
   `claim_side_effects` row in the same transaction, or commit none of them.
2. If a claim row is visible after commit, its required async side-effect record
   is also visible after the same commit.
3. `async_tasks` and `outbox_events` may appear later, but their required
   creation must already be represented by durable committed state.

## Transaction Boundaries

Current synchronous transaction boundary in
`examples/Quan/internal/coupon/repository_claim.go`:

1. Redis hot path admits or rejects the request before MySQL work begins.
2. Lock campaign row with `SELECT ... FOR UPDATE`.
3. Check campaign status/window and MySQL replay state.
4. Count existing user claims.
5. Insert `coupon_claims`.
6. Deduct MySQL stock with `available_stock = available_stock - 1 WHERE available_stock > 0`.
7. Insert `claim_side_effects`.
8. Commit once.

Outside the transaction boundary:

- Redis finalize / rollback after MySQL outcome is known
- side-effect dispatcher creating `async_tasks` and `outbox_events`
- outbox relay publish
- task consume / retry / compensation

These out-of-transaction steps may fail independently. They must not rewrite
the meaning of an already committed claim.

## Guarantees

The current design can defend these statements:

- hotspot contention is serialized at the database row level for correctness
- duplicate request replay with the same idempotency key reuses the same claim
  result
- sold-out requests do not create extra claim rows
- per-user claim limits are enforced on committed rows
- claim and side-effect obligation creation are atomic at commit time

## Non-Guarantees

The current design does not claim:

- Redis as the correctness authority
- exactly-once delivery
- zero contention on hotspot campaigns
- multi-region or multi-primary claim correctness
- instant convergence between Redis and MySQL on every request path
- inline visibility of `async_tasks` and `outbox_events` at claim commit time

## Accepted Tradeoffs

- Redis hot-path admission reserves stock and user quota before MySQL commit;
  the reservation reconciler closes the crash window instead of pretending it
  does not exist.
- Coupon-level hotspot row contention is still accepted at the MySQL stock row
  because committed stock remains the final ledger rail.
- MySQL unique indexes remain defensive rails behind the Redis decision layer.
- Async delivery semantics stay `at-least-once`; duplicate consumption must be
  tolerated by design.
- Side-effect dispatch may lag claim commit, but lack of a dispatcher run does
  not erase the committed obligation recorded in `claim_side_effects`.

## Evidence Mapping

Current correctness claims are backed by these tests:

- replay: `TestRepository_IdempotencySameKeyReturnsSameClaim`
- replay under contention: `TestRepository_ConcurrentReplaySameIdempotencyReturnsSameClaim`
- conflict: `TestRepository_AlreadyClaimedWhenPerUserLimitIsOne`
- sold out: `TestRepository_SoldOutAfterStockExhausted`
- limit reached: `TestRepository_PerUserLimitEnforced`
- hotspot contention / no oversell: `TestRepository_ConcurrentClaimNoOversell`
- side-effect staging without inline task/outbox:
  `TestRepository_ClaimCreatesPendingSideEffectWithoutInlineTaskOutbox`
- side-effect dispatch materialization:
  `TestSideEffectDispatcher_DispatchesPendingClaimSideEffect`
- stale reservation rollback/finalize:
  `TestReservationReconciler_RollsBackExpiredLeaseWithoutPersistedClaim`,
  `TestReservationReconciler_FinalizesExpiredLeaseWhenClaimWasPersisted`

## Current Boundary

The current phase keeps MySQL claim rows as the auditable claim fact while
allowing async follow-up creation to happen later through durable side-effect
staging.
