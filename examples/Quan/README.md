# Quan

**简历 headline**

高冲突场景下的正确性与异步恢复系统

当前版本强调的是：

- Redis 先做热裁决，快速拒绝无效请求
- 受理事实先落 Redis request
- RabbitMQ 保证异步落库链路可恢复
- MySQL 只承载最终 `coupon_claims` 事实
- Reconciler 收敛 publish / consume / finalize / rollback 灰区

**代表性数据**

- 场景：`1000000` 请求，`500` 并发，库存 `10000`
- 同步受理：QPS `3604.53`，P95 `225.44ms`，P99 `324.64ms`
- 返回结果：`202 = 10000`，`409 = 990000`，传输错误 `0`
- 最终结果：MySQL `coupon_claims = 10000`，RabbitMQ 队列排空，无超发

这组数据对应的是“高冲突下 correctness + 异步恢复”能力。

**当前架构**

```text
POST /api/v1/coupons/{coupon_id}/claim
  -> Redis hotpath decide
  -> Redis request store
  -> RabbitMQ publish
  -> consumer async persist coupon_claims
  -> Redis finalize / rollback
  -> GET /api/v1/claim-requests/{request_id}
```

Redis 高可用采用：

- `redis-master`
- `redis-replica`
- `redis-sentinel-1/2/3`

MQ 与持久化采用：

- RabbitMQ durable queue + publisher confirm + manual ack
- MySQL 记录最终 `coupon_claims`

**目录树**

```text
examples/Quan/
├─ main.go                               # Quan 程序入口，装配 MySQL / Redis / RabbitMQ / HTTP / reconciler
├─ README.md                             # 当前架构说明、运行方式、目录说明
├─ config.sample.yaml                    # 唯一保留的示例配置；本地运行和脚本都基于它
├─ bench/                                # benchprep / benchclaim / benchaudit
│  └─ cmd/
│     ├─ benchprep/                      # 准备活动、库存、测试数据
│     ├─ benchclaim/                     # 发起压测请求
│     └─ benchaudit/                     # 三账核对：benchmark / Redis request / MySQL claims
├─ doc/
│  ├─ current-benchmark-summary.md       # 当前唯一保留的 benchmark 摘要
│  └─ request-fault-matrix.md            # 当前 request 架构的故障矩阵与测试分层
├─ internal/
│  ├─ adjudication/
│  │  ├─ hotpath/
│  │  │  ├─ decision.go                  # Redis 热裁决：库存、限领、幂等、PENDING
│  │  │  └─ lease.go                     # finalize / rollback / reservation 管理
│  │  └─ reservation/
│  │     └─ reconciler.go                # 过期 reservation 补偿器
│  ├─ api/
│  │  └─ claim/
│  │     └─ handler.go                   # claim 提交与 request 轮询接口
│  ├─ claim/
│  │  ├─ compat.go                       # claim 域对外导出
│  │  ├─ model/                          # coupon_claims 模型
│  │  ├─ repository/                     # MySQL claim 持久化与查询
│  │  ├─ request/
│  │  │  ├─ types.go                     # request 状态机与接口抽象
│  │  │  ├─ service.go                   # AcceptService / Consumer / QueryService / Reconciler
│  │  │  ├─ store.go                     # Redis request store
│  │  │  ├─ rabbitmq.go                  # RabbitMQ publisher / consumer
│  │  │  ├─ adapters.go                  # hotpath 与 repository 适配层
│  │  │  ├─ component.go                 # background component 封装
│  │  │  ├─ telemetry.go                 # 分段耗时日志
│  │  │  ├─ *_test.go                    # request 主链路单测与集成测试
│  │  └─ service/                        # claim 领域服务支撑代码
│  ├─ observability/
│  │  ├─ metrics.go                      # request-centric Prometheus 指标
│  │  └─ metrics_test.go                 # 指标测试
│  └─ testutil/
│     └─ quanenv/
│        └─ integration.go              # MySQL / Redis / RabbitMQ 集成测试环境
├─ scripts/
│  ├─ start_infra.ps1                    # 启动 MySQL / RabbitMQ / Redis Sentinel 主从
│  ├─ start_quan.ps1                     # 用 config.sample.yaml 启动 Quan
│  ├─ run_bench_claim.ps1                # 通用压测脚本，结果输出到未跟踪目录
│  └─ reproduce_headline.ps1             # 一键复现 headline 数据的实验脚本
└─ sql/                                  # Quan 所需 MySQL migration
```

**运行**

启动依赖：

```powershell
powershell -ExecutionPolicy Bypass -File .\examples\Quan\scripts\start_infra.ps1
```

启动服务：

```powershell
powershell -ExecutionPolicy Bypass -File .\examples\Quan\scripts\start_quan.ps1
```

运行当前 headline 实验：

```powershell
powershell -ExecutionPolicy Bypass -File .\examples\Quan\scripts\reproduce_headline.ps1
```

直接跑测试：

```powershell
go test ./examples/Quan/...
```

**当前文档**

- [current-benchmark-summary.md](doc/current-benchmark-summary.md)
- [request-fault-matrix.md](doc/request-fault-matrix.md)
