## 项目经历
**mini-jupiter｜Go 后端基础框架（net/http）**

沉淀后端工程化底座：运行时托管、配置热更新、结构化日志与 Trace 关联、中间件扩展、统一错误模型与过载保护；提供示例 HTTP 服务与 Prometheus/Grafana 一键观测。

**技术栈**：Go、net/http、Viper、fsnotify、Zap、Prometheus、Grafana、Docker

**核心设计**：
- Runtime/Lifecycle：抽象 Application/Component，统一托管 HTTP Server/Worker Pool；支持 OS Signal 优雅退出与可配置 shutdown timeout，降低业务样板代码
- Config：file/env 覆盖 + 热更新回调；基于 `atomic.Value` 原子替换配置快照，读路径无锁，避免热更新下的数据竞争
- Middleware：洋葱模型链式编排（Recovery/Logging/TraceID/RateLimit/Isolation）；TraceID 在入口生成/透传并注入 logger/context，实现请求-日志链路关联
- Error Model：业务错误码与 HTTP 映射分层；统一 JSON 错误响应（code/message/trace_id），并按业务 code 维度输出错误指标
- Overload Protection：令牌桶限流 + 路由级隔离（并发上限 N、队列上限 M、等待超时 T 均可配置；队列满/超时拒绝）；示例配置：`/ping` N=200、M=400、T=50ms
- Observability：Prometheus 指标（QPS、latency histogram、inflight、error counter）+ Grafana Dashboard；基线压测结果归档：localhost/loopback `/ping`（c=50，n=1,000,000，baseline：middleware/logging/metrics off，响应体 4B）吞吐约 10.9–11.6 万 req/s，P99 约 2.7–2.8ms；开启 Logging+Metrics 后吞吐约 -0.4%，P99 约 +0.03ms（见 bench/results）
