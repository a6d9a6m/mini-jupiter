## 项目经历（简历版）

**项目名称**：mini-jupiter（Go 后端基础框架）  

**项目概述**：  
面向后端服务的轻量级基础框架练手项目，聚焦配置、日志、中间件、错误处理、生命周期管理与可观测性能力，强调工程化抽象与可讲性。

**技术栈**：  
Go、Zap、Viper、Prometheus、Docker

**项目亮点**：  
- 统一入口与依赖隔离：业务仅依赖 `pkg/*`，降低对第三方库耦合  
- 中间件洋葱模型：支持 Recovery/Logging/Trace/RateLimit 可插拔组合  
- 配置热更新 + 并发安全：文件/环境变量覆盖，`atomic.Value` 保证并发一致性  
- 生命周期管理：组件化 Start/Stop + 优雅退出  
- 可观测性基础：结构化日志 + trace_id + Prometheus 指标
