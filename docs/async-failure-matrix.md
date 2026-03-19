# Async Failure Matrix

## Goal

Map each critical async failure path to an explicit recorded state and recovery
mechanism.

## Matrix

| Failure Path | Recorded State | Recovery Mechanism | Final Contract |
| --- | --- | --- | --- |
| Side-effect dispatcher fails before downstream rows are durable | `claim_side_effects.status = PENDING` with retry metadata | Dispatcher retries on next dispatch window | Retryable and visible |
| Side-effect dispatcher gets stuck after claiming work | `claim_side_effects.status = PROCESSING` | Stale-processing recovery scan reopens record to `PENDING` | Recoverable without losing follow-up obligation |
| Side-effect payload is malformed and retry budget is exhausted | `claim_side_effects.status = SUSPENDED` | Manual inspection/fix required | Dead but visible |
| Relay publish fails before queue write | `outbox_events.status = PENDING` with retry metadata | Relay retries on next dispatch window | Retryable and visible |
| Relay publishes successfully but `MarkPublished` fails | `outbox_events.status = DISPATCHING` | Stale-dispatch recovery scan reopens event to `PENDING` | Recoverable with duplicate-publish risk |
| Relay sees malformed payload | `outbox_events.status = SUSPENDED` | Manual inspection/fix required | Dead but visible |
| Consumer handler returns error and retry budget remains | `async_tasks.status = FAILED` with `next_retry_at` | Retry scheduler or compensator enqueues again | Retryable and visible |
| Consumer handler returns error and retry budget is exhausted | `async_tasks.status = DEAD` | Manual replay endpoint | Dead but visible |
| Consumer handler succeeds but `MarkSuccess` fails | `async_tasks.status = SUSPENDED` when fallback update succeeds, otherwise stale `RUNNING` | Compensator converts it back to `FAILED` and schedules retry | Recoverable with duplicate-consume risk |
| Consumer replay reaches a handler that already recorded a unique consume receipt | duplicate write is rejected by `task_consume_receipts` unique key | Handler treats duplicate receipt as a no-op | Duplicate side effect suppressed for covered handlers |
| Queue `ScheduleRetry` fails after task marked `FAILED` | `async_tasks.status = FAILED` and due for retry | Compensator schedules the missing retry later | Retryable and visible |
| Process crashes after task marked `RUNNING` but before a final state update | stale `async_tasks.status = RUNNING` | Compensator converts stale `RUNNING` to `FAILED` and reschedules | Recoverable with duplicate-consume risk |
| DLQ replay fails to move task from Redis DLQ | task restored from `FAILED` back to `DEAD` by service fallback | Manual retry after queue issue is fixed | Dead but visible |

## Test Mapping

- side-effect staging without inline task/outbox: `TestRepository_ClaimCreatesPendingSideEffectWithoutInlineTaskOutbox`
- side-effect dispatcher success path: `TestSideEffectDispatcher_DispatchesPendingClaimSideEffect`
- side-effect dispatcher duplicate-safe redispatch: `TestSideEffectDispatcher_RepeatedDispatchDoesNotDuplicateTaskOrOutbox`
- side-effect stale-processing recovery: `TestSideEffectDispatcher_RecoversStaleProcessingThenDispatches`
- side-effect malformed payload suspend: `TestSideEffectDispatcher_SuspendsMalformedPayloadAtRetryBoundary`
- side-effect dispatcher continues batch after local state-write failures:
  `TestSideEffectDispatcher_TryMarkProcessingFailureContinuesBatch`,
  `TestSideEffectDispatcher_MarkRetryFailureContinuesBatch`,
  `TestSideEffectDispatcher_MarkSuspendedFailureContinuesBatch`
- relay publish failure: `TestRelayDispatchPublisherFailureMarksRetry`
- relay publish success + mark failure: `TestRelayDispatchMarkPublishedFailureLeavesDispatchingForRecovery`
- relay invalid payload: `TestRelayDispatchInvalidPayloadMarksSuspended`
- relay continues batch after local state-write failures:
  `TestRelayDispatchMarkDispatchingFailureContinuesBatch`,
  `TestRelayDispatchMarkRetryFailureContinuesBatch`,
  `TestRelayDispatchMarkSuspendedFailureContinuesBatch`
- consumer success path: `TestConsumeTaskSuccessMarksSuccess`
- consumer success + mark failure: `TestConsumeTaskSuccessMarkSuccessFailureSuspendsTask`
- consumer duplicate side-effect suppression after replay:
  `TestE2E_FaultInjection_ConsumerMarkSuccessFailure_DeduplicatesSideEffectByUniqueKey`
- reconciler continues batch after local lease failures:
  `TestReservationReconciler_LeaseFailureContinuesBatch`,
  `TestReservationReconciler_FinalizeAndRollbackFailureContinueBatch`
- compensator recovers due failed tasks: `TestCompensator_compensateOnce_Success`
- compensator recovers suspended/stale tasks: `TestCompensator_compensateOnce_RecoversSuspendedAndStaleRunning`
- retry schedule failure recovered by compensator: `TestE2E_TaskPipeline_RetryScheduleFailure_RecoveredByCompensation`

## Reading Boundary

These results support only:

- `at-least-once`
- explicit visible async state
- recoverable gray failures within the covered matrix
- duplicate side-effect suppression only for handlers that persist unique
  consume receipts
- best-effort batch scans that continue past one failed item

They do not support:

- `exactly-once`
- zero duplicates
- global duplicate suppression for every handler
- zero operator intervention
