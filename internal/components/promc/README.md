# promc — 内置组件（指标与健康检查）

把 Prometheus 指标暴露装配为生命周期组件：私有 registry（预挂 Go 运行时
与进程 collectors）+ `/metrics` `/health` handler，otel prometheus
导出器实现 **metric.Reader**——埋点面（otel metric API，经 `otel.SetMeterProvider`
安装的全局 MeterProvider）与导出面（client_golang
私有 registry）由此闭环。接线（New）即安装 otel 全局 provider：其后构造的
业务仪表自动路由到本出口，免逐组件穿线。不接线时 otel 全局即 noop
provider，"缺省不埋点"无需额外导出依赖即达成，模板行为不变。

暴露形态缺省为 **handler 注入**（单端口收编）：`addr` 为空（缺省）不自起
独立 server，`MetricsHandler` / `HealthHandler` 由 httpserver 挂进业务
路由（`WithProm`，ADR-0005）；配置 `addr` 才自起独立 HTTP Server。

**第三方依赖**：`github.com/prometheus/client_golang` +
`go.opentelemetry.io/otel/exporters/prometheus` + `go.opentelemetry.io/otel`
（另 import 其嵌套模块 `otel/metric` 与 `otel/sdk/metric`）。
删除本目录并 `go mod tidy` 后即从 go.mod 清除。

## 接入（复制即用，无需 go get——组件已在本模块内）

**组件触点** `internal/app/biz.go`：

```go
pm, err := AddComponent(t, r, promc.SectionName, promc.Default(), promc.New)
if err != nil {
    return err // 配置非法在此报错（退出码 1）
}
// New 即安装 otel 全局 MeterProvider：其后构造的业务仪表自动路由到本出口
// （otel 仪表绑定创建时刻的 provider，promc 须写在业务组件之前）；
// 业务埋点直接走 otel metric API（scope 惯例传调用方 import 路径）：
//   m := otel.GetMeterProvider().Meter("go_template/internal/biz")
//   c, _ := m.Int64Counter("biz_events", metric.WithUnit("1"))
//   c.Add(ctx, 1) // 仪表名不带 _total/_seconds，由导出器按类型与单位追加
// 跨组件健康检查由装配点胶水登记（组件间零依赖）：
// pm.RegisterCheck("nacos", nc.Check)
// 单端口收编（缺省形态）：httpserver 经 WithProm(pm) 挂载 handler，无需操作
```

**配置节**（追加到 `configs/config.yaml`，或由
`config.Dump(os.Stdout, "promc", promc.Default())` 生成）：

```yaml
promc:
  addr: ""          # 独立监听地址；空（缺省）= 不自起 server，handler 由 httpserver 挂载
  metrics_path: /metrics
  health_path: /health
```

**单端口注入**（不接线 httpserver、自行挂到业务路由时）：

```go
mux.Handle(pm.MetricsPath(), pm.MetricsHandler())
mux.Handle(pm.HealthPath(), pm.HealthHandler())
// gin: router.GET("/metrics", gin.WrapH(pm.MetricsHandler()))
// fiber: app.Get("/metrics", adaptor.HTTPHandler(pm.MetricsHandler()))
```

## 行为契约

- **私有 registry**：预挂 `collectors.NewGoCollector`（goroutines、GC、
  内存等运行时指标）与 `NewProcessCollector`（进程 CPU / 内存 / fd）；
  在 prometheus **默认 registry** 注册的自定义 collector 不会出现在
  `/metrics`——一律经 `Registry()`（自定义 Collector）注册，otel 业务
  仪表由导出器自动进本 registry
- **Meter 语义**：埋点面即 otel metric API——仪表构造期创建（禁止
  热路径）、绑定创建时刻的 provider；`WithUnit("1")` / `WithUnit("s")`
  声明单位，导出名由导出器追加 `_total` / `_seconds` 后缀；非法指标名
  构造期报错（不静默改名）
- **默认 provider**：`New` 即 `otel.SetMeterProvider`——其后构造的业务
  仪表自动路由到本出口（仪表绑定创建时刻，已建仪表不迁移、不追溯）；
  多实例构造后者胜，Stop 不回退；因此本组件须先于业务组件接线
- **健康检查**：命名项聚合，全部通过 200、任一失败 503（JSON，details
  含各项状态与错误消息）；零登记恒 healthy；仅接受 GET。跨组件检查由
  装配点胶水经 `RegisterCheck` 登记，组件间互不 import
- **启动**：addr 非空时同步 `net.Listen` 后起服务 goroutine——端口占用
  等绑定错误在启动期暴露（fail-fast，退出码 1）；addr 为空（缺省）跳过
  监听（handler 形态，`MetricsHandler` / `HealthHandler` 照常可用）
- **停机**：Stop 在停机预算内优雅关停（排空在途请求）；失败降级为警告
  日志，幂等可重入；addr 为空时跳过 server 关停，但仍回收 provider
- **fail-fast**：paths 为空、路径不以 `/` 起头、两路径相同，均在构造期
  拒绝
- **并发纪律**：health 检查表 RWMutex 保护（登记串行、请求并发读）；
  registry 由 client_golang 保证并发安全，otel 仪表由 sdk 保证并发安全
- **不热更**：启动期配置，变更重启生效（不实现 ApplyConfig）

## 字段速查

| 字段           | 缺省       | 说明                                        |
|--------------|----------|-------------------------------------------|
| addr         | ""       | 独立监听地址（空 = handler 形态不自起 server，`:0` 时端口内核分配 `Addr()` 取实际值）；变更重启生效   |
| metrics_path | /metrics | 指标暴露路径；须以 `/` 起头；变更重启生效                   |
| health_path  | /health  | 健康检查路径；须以 `/` 起头且与 metrics_path 相异；变更重启生效 |

## 生命周期 API

| API                                                 | 说明                                          |
|-----------------------------------------------------|---------------------------------------------|
| `Default() Config`                                  | 默认值基座（与 `config.Decode` 成对使用）               |
| `(Config).Validate() error`                         | 校验取值（路径非空 / `/` 起头 / 两路径相异）                      |
| `New(cfg Config) (*Prom, error)`                    | 构造 registry 与路由（无副作用、不监听）并安装 otel 全局 MeterProvider；失败无资源需清理 |
| `(*Prom).Start(ctx) error`                          | addr 非空时同步监听（绑定错误 fail-fast）后起服务 goroutine；addr 为空跳过 |
| `(*Prom).Stop(ctx) error`                           | 预算内优雅关停并回收 provider（addr 为空时仍回收）；幂等，失败降级警告   |
| `(*Prom).Meter(scope string) otelmetric.Meter`      | 本组件安装的 provider 下指定 scope 的 otel 仪表面（单实例时即全局；缺省直接用 `otel.GetMeterProvider().Meter(scope)`） |
| `(*Prom).Registry() prometheus.Registerer`          | 私有 registry（自定义 Collector 注册用）              |
| `(*Prom).RegisterCheck(name string, fn CheckFunc)`  | 登记命名健康检查（装配点胶水）                             |
| `(*Prom).MetricsHandler() http.Handler`             | `/metrics` 等价 handler（单端口注入用）               |
| `(*Prom).HealthHandler() http.Handler`              | 健康检查等价 handler（单端口注入用）                      |
| `(*Prom).MetricsPath() / HealthPath() string`       | 配置的暴露路径（挂载方对齐用；httpserver 据此挂载并联动跳过清单） |
| `(*Prom).Addr() string`                             | 实际监听地址（Start 后生效；`:0` 分配时取实际端口；addr 为空恒空） |
| `CheckFunc func() error` / `Status` / `CheckResult` | 健康检查契约：nil = 健康；Status: healthy / unhealthy |
