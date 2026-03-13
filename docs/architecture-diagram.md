# Architecture Diagram

```mermaid
flowchart LR
    Client[Client]
    HTTP[Quan HTTP API]
    MW[Middleware<br/>TraceID / Logging / Recovery]
    Coupon[Coupon Service]
    MySQL[(MySQL<br/>coupon_claims<br/>async_tasks<br/>outbox_events)]
    Redis[(Redis<br/>ready / retry / dlq)]
    Relay[Outbox Relay]
    Consumer[Task Consumer]
    Compensator[Task Compensator]
    Handler[Async Handler]
    Metrics[Prometheus Metrics]
    Grafana[Grafana Dashboard]

    Client --> HTTP --> MW --> Coupon
    Coupon --> MySQL
    Coupon -->|task + outbox commit| MySQL
    Relay -->|scan dispatchable events| MySQL
    Relay -->|publish ready| Redis
    Consumer -->|pop ready| Redis
    Consumer -->|task state transitions| MySQL
    Consumer --> Handler
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
- hot async transport: Redis queue keys
- reliability mechanisms: outbox relay + task consumer + compensator
- observability path: HTTP metrics + business metrics + async recovery metrics
