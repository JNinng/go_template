# 可观测组件进内置库与 observ ctx 演进

内置组件库新增两个可观测组件，并为此对 observ 契约库做了一次破坏性演进：

- **otelc**（OTel 链路追踪）：装配全局 TracerProvider（OTLP gRPC/HTTP
  导出，恒 insecure）；endpoint 为空也安装 provider——trace_id 生成与
  日志关联照常工作，仅不导出。同时为日志注入链路属性：ctx 携带有效
  span 时附加 `trace_id` / `span_id`。
- **promc**（指标与健康检查）：Prometheus 私有 registry（预挂 Go 运行时
  与进程 collectors）经独立 HTTP Server 暴露 `/metrics` `/health`；以
  `observ/adapters/prom` 实现 `observ.Meter`，兑现 DESIGN §9 预留的
  "prom 适配器在装配点注入" 路线。骨架不接线时维持 Noop 缺省不变。

配套决策：

- **observ v0.2.0（破坏性）**：`Logger` 两方法加 ctx，签名与 slog.Logger
  逐字对齐。此前 `Log` 无 ctx，链路上下文在 observ 边界被丢弃，日志
  trace 关联从接口上不可实现。拒绝双轨 `LogCtx` 扩展接口——与 observ
  "刻意极小接口"的立场冲突，且旧面永远拿不到链路信息。v0.x 阶段以
  minor 版本号表达破坏性（生态消费方经扫描确认仅本模板与适配器）。
- **切分两组件而非一个**：tracing 与指标/健康的依赖树、删除理由、
  配置节完全独立——不做链路追踪的项目删除 otelc 后 `go mod tidy`
  即清掉整棵 otel 树，菜单模型的分工由此保持。
- **日志注入做在 observ 边界装饰层，而非 slog handler 层**：handler 级
  包装须接管 `slog.SetDefault`，而 slog 的内部 defaultHandler 写路径经
  stdlib log 桥，`SetDefault` 会把该桥重定向回新默认形成自环——在从未
  设置过具体 handler 的进程里，首条日志即在 log.Logger 的互斥锁上死锁
  （实现期被 otelc 离线测试捕获并修正）。observ 边界装饰零全局接管，
  且与任意后端（slog、zaplog 桥）正交。

## Considered Options

- **单组件包办 tracing + 指标 + 健康**——拒绝：只要指标的项目背上 otel
  全家桶，依赖树不可拆。
- **OTel logs 信号一并接入（日志导出 OTLP）**——首期拒绝：与 slog 单一
  日志面（ADR-0003）的融合是独立设计题（桥进 slog 面需换 otelslog 桥、
  动主日志管线），不搭车。日志已有 zapc 文件管线兜底。
  （后续演进已落地，取 zap core 组合，见末条）
- **trace 注入走 slog handler 包装（`slog.SetDefault` 重包）**——拒绝：
  自环死锁（见上）；且与 zapc 后端组合时接管面互相冲突。
- **OTLP 日志导出（后续演进，已落地）**：首期以"与 slog 面融合是独立
  设计题"为由暂缓；落地时在两个机制间抉择——(a) zap core 组合：otelc
  增日志信号，经 otelzap 桥产出 `zapcore.Core`，装配点以新增的
  `zapc.WithCore` 并进 tee；(b) observ 边界装饰（traceLogger 同款）：
  后端无关但只覆盖 observ 调用面（ADR-0003 恰好祝福"业务直调 zap 的高
  频路径"，该路径会静默漏出海），且被 zapc 热更重建冲掉后日志无声停摆。
  取 (a)：对参考机制最忠实（其前提本就是 zap 主栈）、三条 zap 调用面
  全覆盖、core 是 kit 构建参数故热更重建天然安全、停机顺序顺刃
  （zapc 后停先 Sync、otelc 先接线后 flush）；代价是依赖 zapc 后端
  ——与参考实现同前提，缺省 slog 管线维持零第三方依赖边界。
- **prom 适配器 55 行复制并入 promc**——拒绝：已发布、有测试、有版本的
  同作者小模块走 `go get`（依赖随组件乘车、删除即清），复制切断升级通道；
  "复制即用"指组件代码在内置库，不是拒绝依赖乘车。
- **docker 演示栈（Alloy/Loki/Grafana）一并搬入**——拒绝：独立的体验类
  交付，另行决策；组件本身离线可测。
