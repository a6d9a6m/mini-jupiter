# mini-jupiter

`mini-jupiter` 当前主叙事是 `examples/Quan`：一个面向高冲突优惠券领取场景的正确性与异步恢复系统。

核心链路：

1. Redis 热裁决决定是否受理请求
2. Redis 记录 `claim request`
3. RabbitMQ 持久化投递受理消息
4. consumer 异步写入 MySQL `coupon_claims`
5. Redis `finalize / rollback`
6. 用户轮询 `request` 结果

这个示例聚焦：

- 高冲突下不超发、不重复落库
- 灰区故障最终可恢复
- Redis、RabbitMQ、MySQL 三层状态可对账
- 故障注入、审计脚本和集成测试可以复现这些结论

最能代表当前系统的一组数据：

- 场景：`1000000` 请求，`500` 并发，库存 `10000`
- 同步受理：QPS `3604.53`，P95 `225.44ms`，P99 `324.64ms`
- 返回分布：`202 = 10000`，`409 = 990000`，传输错误 `0`
- 结果收敛：最终 MySQL `coupon_claims = 10000`，无超发，RabbitMQ 队列清空

快速开始：

```powershell
docker compose up -d mysql rabbitmq redis-master redis-replica redis-sentinel-1 redis-sentinel-2 redis-sentinel-3
$env:CONFIG_PATH="examples/Quan/config.sample.yaml"
go run ./examples/Quan
```

常用命令：

```powershell
go test ./examples/Quan/...
go test ./examples/Quan/internal/claim/request -count=1
go test ./examples/Quan/bench/cmd/benchaudit -count=1
```

重点文档：

- [Quan README](examples/Quan/README.md)
- [请求故障矩阵](examples/Quan/doc/request-fault-matrix.md)
- [当前基准摘要](examples/Quan/doc/current-benchmark-summary.md)
