# mini-jupiter

轻量级 Go 后端基础框架学习项目，参考 Jupiter 的设计思路，目标是把配置、日志、中间件、错误处理、生命周期、可观测性、并发治理等“工程化基础能力”做成可复用模块。

## 特性
- 配置管理：文件 + 环境变量覆盖 + 热更新
- 日志系统：结构化日志 + trace_id 注入
- 中间件体系：Recovery / Logging / TraceID / RateLimit / Isolation
- 统一错误：业务错误码 + HTTP 映射 + JSON 响应
- 生命周期：组件化启动/停止 + 优雅退出
- 可观测性：Prometheus 指标 + Grafana 仪表盘
- 并发治理：Worker Pool + 路由级并发隔离（并发/排队/超时）

## 目录结构
```
mini-jupiter/
├─ examples/
│  └─ http-server/            # 示例服务
├─ internal/
│  └─ middleware/             # 中间件链（接入层）
├─ pkg/
│  ├─ config/                 # 配置管理
│  ├─ log/                    # 日志封装
│  ├─ errors/                 # 错误体系
│  ├─ runtime/                # 生命周期管理
│  ├─ metric/                 # Prometheus 指标
│  ├─ pool/                   # Worker Pool
│  ├─ ratelimiter/            # 令牌桶限流
│  └─ isolation/              # 并发隔离（核心逻辑）
├─ grafana/                   # Grafana provision + dashboard
├─ bench/                     # 压测脚本与结果
├─ docker-compose.yml         # Prometheus + Grafana
└─ prometheus.yml             # Prometheus 抓取配置（可选）
```

## 快速开始
```bash
go run ./examples/http-server
```
访问：
- `GET /ping`（正常）
- `GET /api/users`（业务错误）
- `GET /panic`（panic 触发 Recovery）
- `GET /slow`（慢请求）
- `POST /jobs`（异步任务）
- `GET /metrics`（指标）

## 配置说明
示例配置：`examples/http-server/config.yaml`
- `app`：应用信息
- `http.addr`：监听地址
- `log`：日志级别与格式
- `middleware`：中间件开关（Recovery/Trace/Logging）
- `metric`：指标开关与路径
- `ratelimit`：限流参数
- `isolation`：并发隔离（每路由并发/排队/超时）

## 可观测闭环（Prometheus + Grafana）
已补齐以下指标：
- HTTP duration histogram（P95/P99）
- inflight gauge
- error counter（按 code）

错误响应会携带 `trace_id`，便于从接口响应定位到对应日志。

快速启动（Docker Desktop）：
```bash
docker compose up -d
```
访问：
- Prometheus: `http://localhost:9090`
- Grafana: `http://localhost:3000`（admin/admin）

Grafana 已自动 provision：
- 数据源：Prometheus
- Dashboard：`Mini-Jupiter Overview`

说明：项目为学习用途，不考虑线上采样与性能开销的极致优化，仅用于展示“能定位问题”的闭环能力。

## 性能基线压测（hey）
基线配置：`examples/http-server/config.baseline.yaml`（关闭中间件/指标/限流）

安装 hey：
```bash
go install github.com/rakyll/hey@latest
```

运行基线压测：
- Windows：
```powershell
powershell -ExecutionPolicy Bypass -File .\bench\run.ps1
```
- Linux/macOS（有 make 时）：
```bash
make bench
```
输出结果：`bench/results/baseline_*.txt`

## 许可证
MIT License. See `LICENSE`.
