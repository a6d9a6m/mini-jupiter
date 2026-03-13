# Final Reliability Report

## Date

2026-03-13

## Reliability Position

The project now supports this bounded statement:

- within the current deterministic fault-injection matrix, critical async gray
  failures were recovered into visible retry, visible dead-letter state, or
  final success; no tested path ended in silent loss

The project does not support:

- exactly-once delivery
- zero duplicates
- full production failure coverage

## Covered Failure Models

| Failure Model | Evidence | Observed Result |
| --- | --- | --- |
| relay publish failure | `TestE2E_FaultInjection_RelayPublishFailure_RetryThenRecover` | recovered to final success |
| relay publish success + mark-published failure | `TestE2E_FaultInjection_RelayMarkPublishedFailure_RecoveredByDispatchScan` | stale dispatch reopened and completed |
| consumer success + mark-success failure | `TestE2E_FaultInjection_ConsumerMarkSuccessFailure_RecoveredByCompensation` | suspended task recovered and completed |
| retry scheduling failure | `TestE2E_TaskPipeline_RetryScheduleFailure_RecoveredByCompensation` | compensator restored retry flow |
| duplicate ready delivery | `TestE2E_FaultInjection_DuplicateReadyDelivery_ConsumesOnce` | duplicate delivery did not multiply side effects |
| short Redis publish outage | `TestE2E_FaultInjection_ShortRedisOutageOnPublishReady_Recovered` | final success after transient publish failure |
| stale running after restart-like pause | `TestE2E_FaultInjection_StaleRunningRecoveredAfterRestartLikePause` | stale `RUNNING` recovered and completed |
| DLQ replay | `TestE2E_TaskPipeline_DLQReplay_ManualRecover` | dead task manually reopened to success |

## Recovery Timing Evidence

Recovery-after-injected-failure spot check:

- scenario: `recovery_after_injected_failure`
- command:

```powershell
go test ./examples/Quan/internal/task -run TestE2E_FaultInjection_ShortRedisOutageOnPublishReady_Recovered -count=1 -v
```

- observed wall-clock runtime on 2026-03-13: about `1.48s`
- environment: same local Docker-backed test stack as the benchmark run

Interpretation:

- the project can now answer "what happens after a short publish-side Redis
  outage?" with a specific tested path, not with an interview narrative

## Reliability Boundaries

Correct claims:

- MySQL-committed claim/task/outbox state remains the auditable result
- outbox turns publish-side gray failures into explicit recoverable state
- compensator closes suspended and stale-running task paths that pure retries do
  not close
- reliability claims are bounded by explicit tested failure models

Incorrect claims:

- messages are never lost
- duplicate delivery can never happen
- recovery is instantaneous
- all production failures are covered
