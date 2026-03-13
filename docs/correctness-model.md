# Correctness Model

## Scope

Phase 1 defines the correctness boundary for the Quan coupon-claim path without
pulling in Phase 2's Redis decision-layer rewrite.

Current implementation status in this phase:

- claim admission is still serialized by the MySQL transaction path
- MySQL-committed rows define the final auditable result
- Redis is not part of the correctness commit boundary yet
- outbox/task rows are part of the same commit boundary as the claim row

This means Phase 1 is about making guarantees explicit, not about pretending the
Redis hot path is already finished.

## Formal Design Choice

Correctness is judged by MySQL-committed state:

- `coupon_claims` records the committed claim result
- `async_tasks` records the committed async work item
- `outbox_events` records the committed post-claim dispatch work

Redis may cache or accelerate future paths, but Redis contents are allowed to
lag, expire, or be rebuilt. A Redis mismatch does not redefine the final result.

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

### Task and Outbox Invariants

1. A newly committed claim must commit its corresponding `async_tasks` row and
   `outbox_events` row in the same transaction, or commit none of them.
2. If a claim row is visible after commit, its initial async task and outbox
   event are also visible after the same commit.
3. Phase 1 only guarantees creation atomicity for task/outbox rows. It does not
   yet claim that every async transition is explicit; that belongs to Phase 2.

## Transaction Boundaries

Current transaction boundary in
`examples/Quan/internal/coupon/repository.go`:

1. Lock campaign row with `SELECT ... FOR UPDATE`.
2. Check campaign status/window.
3. Check idempotency replay in MySQL.
4. Count existing user claims.
5. Deduct stock with `available_stock = available_stock - 1 WHERE available_stock > 0`.
6. Insert `coupon_claims`.
7. Insert `async_tasks`.
8. Insert `outbox_events`.
9. Commit once.

Outside the transaction boundary:

- writing the Redis idempotency cache in `service.go`
- outbox relay publish
- task consume / retry / compensation

These out-of-transaction steps may fail independently. In Phase 1 they must not
rewrite the meaning of an already committed claim.

## Guarantees

Phase 1 can defend these statements:

- hotspot contention is serialized at the database row level for correctness
- duplicate request replay with the same idempotency key reuses the same claim
  result
- sold-out requests do not create extra claim rows
- per-user claim limits are enforced on committed rows
- claim, task, and outbox creation are atomic at commit time

## Non-Guarantees

Phase 1 does not claim:

- Redis as the correctness authority
- exactly-once delivery
- zero contention on hotspot campaigns
- multi-region or multi-primary claim correctness
- instant convergence between Redis and MySQL on every request path

## Accepted Tradeoffs

- No Redis pre-deduct is used as the correctness basis in Phase 1.
- Coupon-level hotspot row contention is accepted to keep correctness simple and
  explicit before Phase 2.
- MySQL unique indexes remain defensive rails, but the current path still relies
  primarily on transactional checks rather than a Redis decision layer.
- Async delivery semantics stay `at-least-once`; duplicate consumption must be
  tolerated by design.

## Evidence Mapping

Phase 1 correctness claims are backed by these tests:

- replay: `TestRepository_IdempotencySameKeyReturnsSameClaim`
- replay under contention: `TestRepository_ConcurrentReplaySameIdempotencyReturnsSameClaim`
- conflict: `TestRepository_AlreadyClaimedWhenPerUserLimitIsOne`
- sold out: `TestRepository_SoldOutAfterStockExhausted`
- limit reached: `TestRepository_PerUserLimitEnforced`
- hotspot contention / no oversell: `TestRepository_ConcurrentClaimNoOversell`

## Next Phase Boundary

Phase 2 will move the hot-path decision step toward Redis while preserving the
same correctness contract defined here:

- Redis handles admission and replay acceleration
- MySQL remains the committed auditable state
- outbox and task transitions become an explicit state machine
