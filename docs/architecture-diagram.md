# Architecture Diagram

```mermaid
flowchart LR
    Client[Client]
    HTTP[Quan HTTP API]
    MW[Middleware<br/>TraceID / Logging / Recovery]
    Coupon[Coupon Service]
    Decision[Redis Claim Adjudicator]
    MySQL[(MySQL<br/>coupon_claims<br/>claim_side_effects<br/>async_tasks<br/>outbox_events<br/>task_consume_receipts)]
    Redis[(Redis<br/>claim stock / user counts / idem / claim cache<br/>reservation leases<br/>ready / retry / dlq)]
    Reconciler[Reservation Reconciler]
    Dispatcher[Side-Effect Dispatcher]
    Relay[Outbox Relay]
    Consumer[Task Consumer]
    Compensator[Task Compensator]
    Handler[Async Handler]
    Metrics[Prometheus Metrics]
    Grafana[Grafana Dashboard]

    Client --> HTTP --> MW --> Coupon
    Coupon --> Decision
    Decision --> Redis
    Coupon -->|claim + side-effect commit| MySQL
    Reconciler -->|scan expired reservations| Redis
    Reconciler -->|finalize or roll back from claim state| MySQL
    Dispatcher -->|scan PENDING / PROCESSING side effects| MySQL
    Dispatcher -->|create task + outbox and mark DONE| MySQL
    Relay -->|scan dispatchable events| MySQL
    Relay -->|publish ready| Redis
    Consumer -->|pop ready| Redis
    Consumer -->|task state transitions| MySQL
    Consumer --> Handler
    Handler -->|dedupe covered handlers with consume receipts| MySQL
    Compensator -->|scan FAILED / SUSPENDED / stale RUNNING| MySQL
    Compensator -->|reschedule retry| Redis
    HTTP --> Metrics
    Relay --> Metrics
    Consumer --> Metrics
    Compensator --> Metrics
    Metrics --> Grafana
```

## Notes

- correctness boundary: MySQL committed state
- hot-path adjudication: Redis claim decision, claim cache, and reservation keys
- hot async transport: Redis queue keys
- reliability mechanisms: reservation reconciler + side-effect dispatcher +
  outbox relay + task consumer + compensator
- covered handler dedupe: `task_consume_receipts` for handlers that record
  unique consume receipts
- observability path: HTTP metrics + business metrics + relay/consumer/
  compensator metrics; dispatcher and reconciler are not separately instrumented
  yet
