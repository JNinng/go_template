# otelc — 内置组件（OTel 链路追踪）

把 OpenTelemetry 追踪装配为生命周期组件：全局 TracerProvider + OTLP 导出
（gRPC/HTTP，恒 insecure）+ **日志链路注入**——ctx 携带有效 span 的日志
调用自动附加 `trace_id` / `span_id`（经 observ 边界装饰，不接管任何日志
全局）。包名拼 `c` 与 `go.opentelemetry.io/otel` 消解同名（约定见库 README）。

**承重行为**：`endpoint` 为空也安装 TracerProvider——trace_id 生成与日志
关联照常工作，仅不导出；`endpoint` 非空才创建导出器发 span 到 OTLP
collector（本地开发用 Grafana Alloy / otel-collector 皆可）。

**第三方依赖**：`go.opentelemetry.io/otel` + `sdk` + `trace` +
`exporters/otlp/otlptrace`（grpc/http）+ `github.com/jninng/observ`。
删除本目录并 `go mod tidy` 后即从 go.mod 清除（整棵 otel 依赖树随之清零）。
要求 Go 1.25+（otel v1.43 的最低版本）。

## 接入（复制即用，无需 go get——组件已在本模块内）

**组件触点** `internal/app/biz.go`（接在日志后端组件如 zapc 之后）：

```go
tr, err := AddComponent(t, r, "otelc", otelc.Default(),
	func(c otelc.Config) (*otelc.Tracer, error) {
		return otelc.New(c, otelc.WithService(meta.Name, meta.Env)) // 资源标识从应用元数据传入
	})
if err != nil {
	return err // 配置非法在此报错（退出码 1）
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
```

## 行为契约

- **恒建 provider**：endpoint 为空也安装全局 TracerProvider（trace_id 生成
  能力与导出无关）；endpoint 非空才挂批量 SpanProcessor + OTLP 导出器
- **日志链路注入**：构造即以装饰型 observ.Logger 包一层——`Log` 前从 ctx
  读有效 span，附加 `trace_id`/`span_id` 后委托原实现；此后动态读
  `DefaultLogger()` 的调用全部自动携带，无 span 时零属性差异。构造期快照
  持有者（早于本组件拿到 logger 的组件）保持旧面——**本组件接在日志后端
  组件之后**；zapc 热更重建会重绑 observ 默认、使装饰脱落（重接线或重启
  恢复）
- **资源标识不经 yaml**：`WithService(name, env)` 设 `service.name` 与
  `deployment.environment.name`（OTel semantic conventions），装配点从
  应用元数据传入；缺省不设置（span 可用，聚合侧无法区分服务）
- **导出恒 insecure**：面向本地/内网 collector，TLS、凭据、headers、采样
  配置均不在范围（采样走 SDK 缺省 parent-based always-on）
- **停机**：Stop 在停机预算内 flush 未发送的 span（runner 预算：单步 5s /
  总 10s）；flush 失败降级为警告日志，幂等可重入
- **fail-fast**：protocol 非 grpc/http 在构造期拒绝
- **并发纪律**：装饰层只做属性追加后委托，无共享可变状态；provider 与
  exporter 由 otel SDK 保证并发安全
- **不热更**：启动期配置，变更重启生效（不实现 ApplyConfig）
- **范围外**：OTel logs 信号（日志导出 OTLP）、链路传播中间件
  （otelhttp 等，模板无 HTTP server）——前者与 slog 面的融合是独立决策

## 字段速查

| 字段 | 缺省 | 说明 |
|---|---|---|
| endpoint | ""（不导出） | OTLP collector 地址 host:port；变更重启生效 |
| protocol | grpc | grpc / http；变更重启生效 |

## 生命周期 API

| API | 说明 |
|---|---|
| `Default() Config` | 默认值基座（与 `config.Decode` 成对使用） |
| `(Config).Validate() error` | 校验取值（protocol 仅 grpc/http） |
| `New(cfg Config, ...Option) (*Tracer, error)` | 构造即装配全局 provider + 装饰日志面；失败即未启动，无资源需清理 |
| `WithService(name, env string) Option` | 设置 span 资源标识（service.name / deployment.environment.name） |
| `(*Tracer).Start(ctx) error` | 输出启动信号（含 endpoint / export 状态）后立即返回 |
| `(*Tracer).Stop(ctx) error` | 预算内 flush span；幂等，失败降级警告 |
