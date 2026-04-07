# 当前基准摘要

这份摘要只保留当前架构的一组代表性结果，用于说明系统在高冲突场景下的正确性与异步恢复能力。

## Headline 数据

- 场景：`1000000` 请求，`500` 并发，库存 `10000`
- 请求模式：唯一用户、唯一幂等键
- 架构：Redis Sentinel 主从 + RabbitMQ durable queue + MySQL 最终落库

结果：

- 同步受理 QPS：`3604.53`
- 平均延迟：`139.84ms`
- P95：`225.44ms`
- P99：`324.64ms`
- 返回分布：`202 = 10000`，`409 = 990000`
- 传输错误：`0`
- 最终 MySQL `coupon_claims = 10000`
- RabbitMQ 队列最终排空
- 结论：无超发，最终事实与受理结果收敛一致

这个结果表达的是：

- 大多数冲突请求在 Redis 热裁决阶段被快速拒绝
- 真正受理的 `10000` 个请求经过 RabbitMQ 和异步 consumer 最终全部落成 SQL
- 系统关注点是 correctness 和 recoverability，不是单纯拉高成功路径吞吐

## 复现方式

```powershell
powershell -ExecutionPolicy Bypass -File .\examples\Quan\scripts\reproduce_headline.ps1
```

脚本会自动执行：

1. 启动 MySQL、RabbitMQ、Redis Sentinel 主从
2. 用 `config.sample.yaml` 启动 Quan
3. 准备活动与库存
4. 运行高冲突压测
5. 运行 `benchaudit` 做三账核对

压测结果输出到：

```text
examples/Quan/artifacts/bench/
```
