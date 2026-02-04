# mini-jupiter

一个轻量级 Go 后端基础框架练手项目，参考 Jupiter 的思路，目标是把配置、日志、中间件、错误处理、生命周期、指标、并发控制等“工程化基础能力”做成可复用模块，便于面试讲清楚设计与取舍。

## 特性
- 配置管理：文件 + 环境变量覆盖 + 热更新
- 日志系统：结构化日志 + trace_id 注入
- 中间件体系：Recovery / Logging / TraceID / RateLimit
- 统一错误：业务错误码 + HTTP 映射 + JSON 响应
- 生命周期：组件化启动/停止 + 优雅退出
- 指标监控：Prometheus /metrics
- 并发控制：Worker Pool 示例

## 目录结构
```
mini-jupiter/
├─ examples/
│  └─ http-server/            # 示例服务
├─ pkg/
│  ├─ config/                 # 配置管理
│  ├─ log/                    # 日志封装
│  ├─ middleware/             # 中间件链
│  ├─ errors/                 # 错误体系
│  ├─ runtime/                # 生命周期管理
│  ├─ metric/                 # Prometheus 指标
│  ├─ pool/                   # Worker Pool
│  └─ ratelimiter/            # 令牌桶限流
└─ prometheus.yml             # Prometheus 抓取配置（可选）
```

## 快速开始
```bash
go run ./examples/http-server
```
访问：
- `GET /ping`
- `GET /api/users`
- `POST /jobs`
- `GET /metrics`

## 配置说明
示例配置：`examples/http-server/config.yaml`
- `app`：应用信息
- `http.addr`：监听地址
- `log`：日志级别与格式
- `middleware`：中间件开关（Recovery/Trace/Logging）
- `metric`：指标开关与路径
- `ratelimit`：限流参数

## 性能基线压测（hey）
基线配置：`examples/http-server/config.baseline.yaml`（关闭中间件/指标/限流）

安装 hey：
```bash
go install github.com/rakyll/hey@latest
```

运行基线压测：
```bash
make bench
```
输出结果：`bench/results/baseline_*.txt`

## 设计要点（面试可讲）
- 统一入口与依赖隔离：业务只依赖 `pkg/*`，底层库可替换
- 中间件洋葱模型：请求级能力可插拔
- 配置热更新 + 并发安全：`atomic.Value` + 回调
- 生命周期管理：组件接口统一 Start/Stop
- 可观测性基础：日志 + 指标 + trace_id

## Prometheus（可选）
如需在 Docker Desktop 中抓取：
```yaml
global:
  scrape_interval: 5s

scrape_configs:
  - job_name: "mini-jupiter"
    static_configs:
      - targets: ["host.docker.internal:8080"]
```
启动：
```bash
docker run --name prometheus -d -p 9090:9090 \
  -v $PWD/prometheus.yml:/etc/prometheus/prometheus.yml \
  prom/prometheus
```

## 许可证
MIT License. See `LICENSE`.
