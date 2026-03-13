# Fault Injection Matrix

## Goal

Give a concrete answer to:

- which failures are covered
- how each failure is injected
- what recovery result is expected
- which command reruns the evidence

## Matrix

| Failure Model | Injection Point | Test | Expected Result |
| --- | --- | --- | --- |
| Relay publish failure | `PublishReady` fails once | `TestE2E_FaultInjection_RelayPublishFailure_RetryThenRecover` | outbox retries, task reaches `SUCCESS` |
| Relay publish success but mark-published failure | relay repository `MarkPublished` fails once | `TestE2E_FaultInjection_RelayMarkPublishedFailure_RecoveredByDispatchScan` | stale `DISPATCHING` recovered, outbox reaches `PUBLISHED` |
| Consumer success but status update failure | task repository `MarkSuccess` fails once | `TestE2E_FaultInjection_ConsumerMarkSuccessFailure_RecoveredByCompensation` | task becomes `SUSPENDED`/recovered, final `SUCCESS` |
| Retry scheduling failure | queue `ScheduleRetry` fails once | `TestE2E_TaskPipeline_RetryScheduleFailure_RecoveredByCompensation` | compensator restores retry flow, final `SUCCESS` |
| Duplicate delivery / duplicate consume | same task ID pushed twice to ready queue | `TestE2E_FaultInjection_DuplicateReadyDelivery_ConsumesOnce` | handler side effect still runs once |
| Short Redis outage simulation | `PublishReady` fails multiple consecutive times | `TestE2E_FaultInjection_ShortRedisOutageOnPublishReady_Recovered` | relay keeps retrying until queue recovers |
| Crash/restart-like stale running | task pre-marked `RUNNING` and left unfinished | `TestE2E_FaultInjection_StaleRunningRecoveredAfterRestartLikePause` | compensator reopens stale `RUNNING`, final `SUCCESS` |
| DLQ manual recovery | handler forced to fail until `DEAD`, then replay | `TestE2E_TaskPipeline_DLQReplay_ManualRecover` | final `SUCCESS` after replay |

## Commands

Run all recovery-validation E2E tests:

```powershell
go test ./examples/Quan/internal/task -run TestE2E_ -v
```

Run relay unit fault tests:

```powershell
go test ./examples/Quan/internal/outbox -run TestRelay -v
```

Run consumer and compensator unit fault tests:

```powershell
go test ./examples/Quan/internal/task -run "Test(ConsumeTask|Compensator_)" -v
```

## Boundary

These tests support only:

- deterministic failure reproduction inside the covered matrix
- recoverable async gray failures
- `at-least-once` behavior with visible state

They do not support:

- `exactly-once`
- full production failure coverage
- zero duplicates under all conditions
