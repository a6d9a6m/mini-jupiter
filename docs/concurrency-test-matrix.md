# Concurrency Test Matrix

## Goal

Map every Phase 1 correctness claim to a concrete regression test.

## Matrix

| Scenario | Invariant Covered | Test |
| --- | --- | --- |
| Same request replayed with same idempotency key | Same request returns the same committed claim and does not deduct stock twice | `TestRepository_IdempotencySameKeyReturnsSameClaim` |
| Same request replayed concurrently on a hotspot campaign | Replay stays stable under contention and only one claim row is committed | `TestRepository_ConcurrentReplaySameIdempotencyReturnsSameClaim` |
| Same user sends a new request after already claiming when `per_user_limit=1` | Conflict is explicit and stock is not deducted twice | `TestRepository_AlreadyClaimedWhenPerUserLimitIsOne` |
| Same user exceeds a multi-claim limit | Committed claim count never exceeds `per_user_limit` | `TestRepository_PerUserLimitEnforced` |
| Campaign stock is exhausted | Sold-out requests do not create additional claim rows | `TestRepository_SoldOutAfterStockExhausted` |
| Many users compete for the same limited stock | Committed stock never goes negative and total claims never exceed stock | `TestRepository_ConcurrentClaimNoOversell` |
| Many users compete with `per_user_limit > 1` and ample stock | Per-user limit holds under concurrent pressure | `TestRepository_ConcurrentMultiUserLimitTwoNoOverflow` |
| Mixed stock bottleneck and per-user limit pressure | Stock and limit invariants continue to hold together | `TestRepository_ConcurrentStockAndLimitMixedNoOverflow` |

## Commands

Run coupon correctness tests only:

```powershell
go test ./examples/Quan/internal/coupon -run TestRepository_ -v
```

Integration environment expected by these tests:

- `QUAN_TEST_MYSQL_DSN`

## Reading the Results

Phase 1 is considered complete only if the matrix still proves:

- replay
- conflict
- sold out
- limit reached
- hotspot contention without oversell
