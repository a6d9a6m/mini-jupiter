# Async Reliability State Machine

## Scope

Phase 2 makes the async path explicit for:

- `claim_side_effects`
- `outbox_events`
- `async_tasks`
- side-effect dispatcher
- relay
- consumer
- compensator

The delivery contract is `at-least-once`, not `exactly-once`.

## Claim Side-Effect State Machine

### States

- `PENDING`: claim committed and follow-up work still needs to be created
- `PROCESSING`: dispatcher claimed the side effect and is creating downstream
  records
- `DONE`: required task and outbox records were durably created
- `SUSPENDED`: dispatcher exhausted retry budget or the payload is not safe to
  keep retrying

### Transitions

- `PENDING -> PROCESSING`
  Trigger: dispatcher claims the side effect with `TryMarkProcessing`
- `PROCESSING -> DONE`
  Trigger: task and outbox rows are created and `MarkDone` succeeds
- `PROCESSING -> PENDING`
  Trigger: dispatcher fails and schedules retry metadata
- `PROCESSING -> SUSPENDED`
  Trigger: retry budget is exhausted or payload is not safe to keep retrying
- `PROCESSING -> PENDING`
  Trigger: stale processing recovery scan reopens a stuck record

### Notes

- Dispatcher scans are best-effort per item; one mark/retry/suspend failure does
  not stop later side effects in the same batch.
- Duplicate-safe redispatch relies on idempotent downstream creation plus
  duplicate checks on task/outbox materialization.

## Task State Machine

### States

- `PENDING`: task has been committed but not yet claimed by a worker
- `RUNNING`: a worker has claimed the task and is executing the handler
- `FAILED`: execution failed and the task is retryable
- `SUSPENDED`: execution reached an ambiguous or incomplete state and requires
  recovery
- `SUCCESS`: handler outcome has been committed as successful
- `DEAD`: retries are exhausted; the task is terminal but visible

### Transitions

- `PENDING -> RUNNING`
  Trigger: worker pops ready queue and `TryMarkRunning` succeeds
- `RUNNING -> SUCCESS`
  Trigger: handler succeeds and `MarkSuccess` succeeds
- `RUNNING -> FAILED`
  Trigger: handler fails and retry budget remains
- `RUNNING -> DEAD`
  Trigger: handler fails and retry budget is exhausted
- `RUNNING -> SUSPENDED`
  Trigger: handler succeeds but `MarkSuccess` fails
- `FAILED -> RUNNING`
  Trigger: retry scheduler or compensator re-enqueues the task and a worker
  claims it
- `SUSPENDED -> FAILED`
  Trigger: compensator recovers suspended work for retry
- `DEAD -> FAILED`
  Trigger: manual replay endpoint reopens a dead task

### Notes

- `FAILED` means retryable, not terminal.
- `SUSPENDED` is intentionally visible because the system cannot claim the task
  finished cleanly.
- Handler implementations must be idempotent because replay can happen after
  publish ambiguity, stale `RUNNING` recovery, suspended recovery, or DLQ
  replay.

## Outbox State Machine

### States

- `PENDING`: event is ready to be dispatched
- `DISPATCHING`: relay has claimed the event and is attempting publish
- `PUBLISHED`: publish result has been durably recorded
- `SUSPENDED`: event is malformed or otherwise not safe to keep retrying

### Transitions

- `PENDING -> DISPATCHING`
  Trigger: relay claims the event with `TryMarkDispatching`
- `DISPATCHING -> PUBLISHED`
  Trigger: publish succeeds and `MarkPublished` succeeds
- `DISPATCHING -> PENDING`
  Trigger: publish fails and relay records retry metadata
- `PENDING|DISPATCHING -> SUSPENDED`
  Trigger: payload is malformed or logically invalid
- `DISPATCHING -> PENDING`
  Trigger: stale dispatch recovery scan reopens an event whose publish outcome
  was not durably recorded

### Notes

- `publish success + mark published failure` is treated as publish ambiguity.
- Recovery reopens stale `DISPATCHING` events to `PENDING`.
- Re-dispatch is acceptable because downstream task consumption is only
  `at-least-once`.
- Relay scans are best-effort per event; one state-write failure is logged and
  later events in the batch still continue.

## Component Responsibilities

- Relay:
  claims `PENDING` outbox events, publishes them, and records publish outcome
- Side-Effect Dispatcher:
  claims `PENDING` claim side effects, creates task/outbox rows, and records
  completion or retry state
- Consumer:
  claims retryable tasks, runs handlers, and records success/failure outcome
- Compensator:
  recovers due `FAILED`, stale `RUNNING`, and `SUSPENDED` tasks back into the
  retry flow
- Reservation Reconciler:
  scans expired Redis reservation leases and finalizes or rolls them back from
  persisted MySQL claim facts

## What Phase 2 Guarantees

- No critical async transition is only implicit in application memory
- a committed claim's required async follow-up is represented durably even
  before task/outbox rows exist
- publish ambiguity is visible as `DISPATCHING` and recoverable by scan
- success-update ambiguity is visible as `SUSPENDED` or stale `RUNNING`
- every critical failure path ends in one of:
  - recovered for retry
  - retryable and visible
  - dead but visible

## What Phase 2 Still Does Not Guarantee

- exactly-once delivery
- never-duplicate publish
- never-duplicate consume
- full coverage of every real production failure mode
- exactly-zero lag between claim commit and task/outbox materialization
