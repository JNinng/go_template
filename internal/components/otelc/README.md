# otelc — 内置组件（OTel 可观测组件）

把 OpenTelemetry 装配为生命周期组件：全局 TracerProvider + OTLP 导出
（gRPC/HTTP，恒 insecure）+ **日志链路注入**（ctx 携带有效 span 的日志
调用自动附加 `trace_id` / `span_id`，经 observ 边界装饰，不接管任何日志
全局）+ 可选 **OTLP 日志导出**（`logs_enabled`，经 otelzap 桥以 zap core
形态由装配点组合进 zapc 的 tee）。包名拼 `c` 与
go.opentelemetry.io/otel 消解同名（约定见库 README）。

**承重行为**：`endpoint` 为空也安装 TracerProvider——trace_id 生成与日志
关联照常工作，仅不导出；`endpoint` 非空才创建导出器发 span 到 OTLP
collector（本地开发用 Grafana Alloy / otel-collector 皆可）。

**第三方依赖**：`go.opentelemetry.io/otel` + `sdk` + `sdk/log` + `trace` +
`exporters/otlp/otlptrace`（grpc/http）+ `exporters/otlp/otlplog`（grpc/http）+
`contrib/bridges/otelzap`（zap 桥）+ `github.com/jninng/observ`。
删除本目录并 `go mod tidy` 后即从 go.mod 清除（整棵 otel 依赖树随之清零；
zap 桥所需 zap 已随 zapc 组件在树内）。
要求 Go 1.25+（otel v1.43 的最低版本）。

## 接入（复制即用，无需 go get——组件已在本模块内）

**组件触点** `internal/app/biz.go`（链路注入不限接线顺序；启用
logs_enabled 时须先于 zapc 接线取 LogCore，见下）：

```go
tr, err := AddComponent(t, r, "otelc", otelc.Default(),
	func(c otelc.Config) (*otelc.Tracer, error) {
		return otelc.New(c, otelc.WithService(meta.Name, meta.Env, version.Version)) // 资源标识：应用元数据 + 构建元数据
	})
if err != nil {
	return err // 配置非法在此报错（退出码 1）
}
```

**OTLP 日志导出的组合接线**（`logs_enabled: true` 时）——otelc 先接线、
zapc 经闭包把 `tr.LogCore()` 并进 tee（未启用时 LogCore 为 nil，
`WithCore(nil)` 被忽略，接线无需分支）：

```go
logc, err := AddComponent(t, r, "zapc", zapc.Default(),
	func(c zapc.Config) (*zapc.Log, error) {
		return zapc.New(c, zapc.WithCore(tr.LogCore()))
	})
if err != nil {
	return err
}
```

业务代码取 tracer 直接用全局：`otel.Tracer("your-scope").Start(ctx, "op")`
（provider 已由组件安装；组件删除后该调用退化为 no-op tracer，业务代码
不用改）。

**配置节**（追加到 `configs/config.yaml`，或由
`config.Dump(os.Stdout, "otelc", otelc.Default())` 生成）：

```yaml
otelc:
  endpoint: "" # OTLP collector 地址 host:port；空 = 不导出（trace_id 照常生成）
  protocol: grpc # grpc | http（与 collector 的传输协议）
  logs_enabled: false # OTLP 日志导出（依赖 zapc 后端，经 LogCore 组合）；true 需 endpoint 非空
```

## 行为契约

- **恒建 provider**：endpoint 为空也安装全局 TracerProvider（trace_id 生成
  能力与导出无关）；endpoint 非空才挂批量 SpanProcessor + OTLP 导出器
- **日志链路注入**：构造即以装饰型 observ.Logger 包一层——`Log` 前从 ctx
  读有效 span，附加 `trace_id`/`span_id` 后委托原实现；此后动态读
  `DefaultLogger()` 的调用全部自动携带，无 span 时零属性差异。构造期快照
  持有者（早于本组件拿到 logger 的组件）保持旧面。装饰实现
  `Rebind(observ.Logger)` 协议：zapc 接管与热更重建时经协议原地重绑
  后端，装饰持续有效、接线顺序不受限
- **资源标识不经 yaml**：`WithService(name, env, version)` 设 `service.name`、
  `deployment.environment.name` 与 `service.version`（OTel semantic
  conventions），装配点从应用元数据与构建元数据（`pkg/version`）传入；
  缺省不设置（span 可用，聚合侧无法区分服务）
- **导出恒 insecure**：面向本地/内网 collector，TLS、凭据、headers、采样
  配置均不在范围（采样走 SDK 缺省 parent-based always-on）
- **OTLP 日志导出（logs_enabled）**：与 tracing 共用 endpoint/protocol，
  经 otelzap 桥产出 zap core，由装配点以 `zapc.WithCore` 并进 tee——
  进入 zap 的每条日志（`zap.L()` / kit / observ 三条调用面）自动出海。
  **依赖 zapc 后端**：缺省 slog 管线没有此通路（与参考实现同前提）；
  core 是 kit 构建参数，zapc 热更重建自动带上；停机顺序自然衔接——
  zapc 后接线先停止（Sync 只刷本地 sink——记录在写入时已同步入队
  batch），本组件先接线后停 flush 出海。出海流不受 zapc 级别门控（AtomicLevel 只作用于文件/控制台
  core），级别裁剪交给 collector 侧。启用而无 endpoint 在构造期拒绝
  （日志没有"本地生成"的退化语义，不同于 trace）
- **停机**：Stop 在停机预算内 flush 未发送的日志与 span（日志先于
  tracing）；flush 失败降级为警告日志，幂等可重入
- **fail-fast**：protocol 非 grpc/http 在构造期拒绝
- **并发纪律**：装饰层只做属性追加后委托，无共享可变状态；provider 与
  exporter 由 otel SDK 保证并发安全
- **不热更**：启动期配置，变更重启生效（不实现 ApplyConfig）
- **范围外**：链路传播中间件（otelhttp 等，模板无 HTTP server）、
  TLS/凭据/headers/采样配置（恒 insecure，SDK 缺省采样）

## 字段速查

| 字段           | 缺省      | 说明                                              |
|--------------|---------|-------------------------------------------------|
| endpoint     | ""（不导出） | OTLP collector 地址 host:port；变更重启生效              |
| protocol     | grpc    | grpc / http；变更重启生效                              |
| logs_enabled | false   | OTLP 日志导出（依赖 zapc 后端）；true 需 endpoint 非空；变更重启生效 |

## 生命周期 API

| API                                           | 说明                                                             |
|-----------------------------------------------|----------------------------------------------------------------|
| `Default() Config`                            | 默认值基座（与 `config.Decode` 成对使用）                                  |
| `(Config).Validate() error`                   | 校验取值（protocol 仅 grpc/http）                                     |
| `New(cfg Config, ...Option) (*Tracer, error)` | 构造即装配全局 provider + 装饰日志面（logs_enabled 时另建日志导出管线）；失败即未启动，无资源需清理 |
| `WithService(name, env, ver string) Option`   | 设置资源标识（service.name / deployment.environment.name / service.version），span 与日志共用 |
| `(*Tracer).LogCore() zapcore.Core`            | OTLP 日志导出的 zap core（未启用时 nil）；装配点经 `zapc.WithCore` 组合进 tee     |
| `(*Tracer).Start(ctx) error`                  | 输出启动信号（含 endpoint / export 状态）后立即返回                            |
| `(*Tracer).Stop(ctx) error`                   | 预算内 flush span；幂等，失败降级警告                                       |
