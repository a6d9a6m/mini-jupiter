# Claim Request 新故障矩阵与测试分层

本文档定义 Quan 当前新主链路下的故障矩阵与测试分层。

当前主链路：

1. Redis `Decide`
2. Redis request `Create / UpdateStatus`
3. RabbitMQ `PublishAccepted`
4. RabbitMQ consumer `ConsumeAccepted`
5. SQL `PersistClaimAsync`
6. Redis `Finalize / Rollback`
7. Request `Reconciler`
8. 用户轮询 request 结果

本文档的目标不是记录已实现的单条测试，而是定义：

- 新结构下应该围绕什么对象做故障验证
- 如何把故障矩阵扩展成 400+ 可执行用例
- 每一层测试应该验证什么，不应该验证什么

## 1. 测试对象

新结构下，所有故障验证都围绕这四个对象展开：

1. Redis request ledger
2. RabbitMQ publish / delivery
3. SQL `coupon_claims`
4. Redis hotpath `PENDING / SUCCESS / ROLLED_BACK / reservation`

判断标准是：

- request 是否最终收敛
- 最终 claim 是否正确
- Redis 热态是否最终收口
- MQ 故障后是否可恢复

## 2. 八类故障域

### 2.1 热裁决故障

关注点：

- `Decide` 是否返回正确业务结果
- `CampaignMiss -> EnsureCampaign -> retry Decide` 是否收敛
- 幂等命中、已领、超限、售罄、活动失效是否正确返回
- Redis Sentinel 切主时是否还能继续裁决
- `WAIT` 超时是否会影响同步受理语义

典型用例：

- Lua 返回 admitted
- Lua 返回 sold out
- Lua 返回 limit reached
- Lua 返回 pending
- 主从切换后第一次 `Decide` 重试成功

### 2.2 Request 建档故障

关注点：

- `Create` 是否真正把 request 作为同步事实写入 Redis
- idem 索引和 request 主记录是否一致
- 状态索引是否正确
- `WAIT` 失败时是否会暴露为同步失败
- request 状态是否会被终态回退保护

典型用例：

- request 首次创建成功
- request 重复创建命中 idem
- create 后切主 request 不丢失
- terminal status 不会被回写成中间态

### 2.3 MQ 发布故障

关注点：

- 发布失败时 request 是否停在 `PUBLISHING`
- confirm timeout / nack / channel close 后是否能由 reconcile 收敛
- RabbitMQ broker 重启后 publisher 是否恢复
- topology 已声明时发布路径是否稳定

典型用例：

- publish 成功 + confirm ack
- publish 网络失败
- confirm timeout
- publish nack
- broker restart 后再次 publish

### 2.4 MQ 消费故障

关注点：

- durable queue 是否支持 consumer 延迟启动
- nack/requeue 后是否能重复投递
- consumer 崩溃后消息是否留在 broker
- 多 worker 下是否不会造成最终重复 claim

典型用例：

- delayed consumer drains queue
- transient consume error -> redelivery
- consumer restart 后继续消费
- duplicate delivery 最终只收敛成一个 claim

### 2.5 SQL 落库故障

关注点：

- `PersistClaimAsync` 是否幂等
- 唯一键冲突时是否正确复用已有 claim
- DB transient error 是否会走 retryable path
- DB 非重试错误是否走 rollback path
- 死锁、超时、连接中断是否能被 recover/retry

典型用例：

- insert claim success
- unique conflict reuse existing claim
- transient db unavailable
- deadlock
- connection reset

### 2.6 Finalize / Rollback 故障

关注点：

- SQL 成功但 finalize 失败时 request 是否保留为可恢复态
- SQL 失败但 rollback 失败时是否保留为可恢复态
- finalize 后 Redis `PENDING -> SUCCESS`
- rollback 后 Redis 热库存、用户计数、idem、reservation 是否回收

典型用例：

- finalize success
- finalize timeout -> reconciler repair
- rollback success
- rollback timeout -> reconciler repair

### 2.7 Reconciler 故障

关注点：

- stale `ACCEPTED / PUBLISHING` 是否会补 publish
- stale `ENQUEUED` 是否会重投
- stale `PROCESSING` 且 SQL 已有 claim 是否会补 finalize
- stale `PROCESSING` 且 SQL 无 claim 是否会重投
- reconcile 自身失败后下轮是否还能继续

典型用例：

- accepted stale -> enqueued
- publishing stale -> enqueued
- processing stale + claim found -> succeeded
- processing stale + no claim -> enqueued

### 2.8 查询与用户语义故障

关注点：

- 查询接口是否永远按 request 语义返回
- 中间态是否统一映射为 `PROCESSING`
- 终态是否正确映射 `SUCCEEDED / FAILED`
- request 不存在是否返回 404
- 不允许对用户提前暴露假成功

典型用例：

- accepted -> processing
- succeeded -> succeeded + claim_id
- rolled_back -> failed
- failed -> failed
- request not found

## 3. 四层测试分层

为了扩展到 400+ 用例，建议按四层测试拆分。

### 3.1 单元层

目标：

- 纯状态机和分支逻辑验证
- 不依赖真实 Redis / RabbitMQ / MySQL

适合覆盖：

- `AcceptService`
- `Consumer`
- `Reconciler`
- `QueryService`
- 指标埋点结果

这层可以占最多用例数，适合扩到 200+。

### 3.2 组件集成层

目标：

- 单个外部依赖 + 一个业务组件的语义验证

例如：

- Redis request store
- RabbitMQ publisher / consumer
- claim repository
- hotpath adjudicator
- reservation reconciler

这层适合扩到 100+。

### 3.3 流程 E2E 层

目标：

- 从 accept 到最终收敛的整链路验证

适合覆盖：

- broker restart
- delayed consumer
- finalize failure 后 reconcile
- transient DB error -> redelivery -> succeed
- request stale recovery

这层适合控制在 30~80 条高价值场景。

### 3.4 故障演练层

目标：

- 更接近真实环境的故障注入

例如：

- Redis Sentinel 切主
- RabbitMQ broker restart
- Docker stop/start consumer
- 网络中断 / 超时注入
- DB 资源限流 / deadlock 注入

这层用例数不需要多，但要稳定、可回归，适合 10~30 条。

## 4. 如何扩成 400+ 用例

方法不是堆 400 条 E2E，而是做“维度相乘”。

可以用下面的组合方式：

1. 8 个故障域
2. 每个故障域 5~10 个核心场景
3. 每个场景按 4 个测试层分布
4. 再按 2~3 个配置变体扩展

示意：

- 8 个故障域
- 每域 8 个子场景
- 平均每个子场景 6 个具体测试

总量：

- `8 * 8 * 6 = 384`

再补：

- 压测后审计场景
- 多 worker 变体
- Redis simple / Sentinel 变体

很容易超过 400。

## 5. 建议的测试命名规则

为了后续扩展不混乱，建议统一命名：

- 单元：
  `TestAcceptService_*`
  `TestConsumer_*`
  `TestReconciler_*`

- 组件集成：
  `TestRedisRequestStore_*`
  `TestRabbitMQPublisher_*`
  `TestClaimRepository_*`
  `TestAdjudicator_*`

- 流程 E2E：
  `TestAsyncFlow_*`

- 故障演练：
  `TestChaos_*`
  `TestFailover_*`

## 6. 当前已经覆盖的高价值场景

当前仓库已经有一部分新链路测试基础，主要集中在：

- request 服务层单测
- Redis / RabbitMQ 集成测试
- RabbitMQ broker restart
- finalize failure 后 reconcile
- transient consumer failure 后 redelivery
- delayed consumer drain queue

后续新增时，应优先补目前还薄弱的部分：

- Redis Sentinel 真切主演练
- SQL deadlock / transient DB fault
- request stale 审计与回归
- 高并发下 request 与 claim 三账审计
- 指标驱动的 recoverability 断言

## 7. 新测试体系的最终判断标准

所有新测试最终都应围绕这几个结论落地：

1. request 不会无故消失
2. request 最终会收敛到终态或明确可恢复态
3. `SUCCEEDED` request 必须对应最终 claim
4. `FAILED / ROLLED_BACK` request 不应对应脏成功
5. Redis 热态最终必须 finalize 或 rollback
6. MQ 故障只应造成延迟，不应造成不可恢复灰区

只要测试能稳定证明这 6 条，新结构下的“400+ 故障任务”体系就是成立的。
