# httpserver 可观测聚合：单端口收编、接口化强绑定与 Link 语义

内置组件库新增 **httpserver**（业务 HTTP Server）：标准库 ServeMux 路由
+ 预置可观测中间件链（Recovery → CORS → 访问日志/指标 → otelhttp →
RequestID → 请求体上限）。它消费模板全部可观测设施，四个非常规决策
在此记清。

## 决策

- **单端口收编（promc.addr 缺省空）**：promc 的 `/metrics` `/health`
  经 handler 挂进业务路由，一个端口承载全部——k8s 场景少暴露一个端口、
  网络策略少写一条。实现取"promc.addr 为空即不自起独立 server"而非
  `enabled` 开关：单一旋钮，"handler 库 + 可选独立 server"的语义自然
  涌现。promc 原缺省 `:9090` 是破坏性变更（模板定位可接受；需要独立
  端口显式配置）。
- **强绑定走全局单例 + 接口化注入口**：httpserver 消费 zapc（`zap.L()`）、
  otelc（otel 全局 TracerProvider）经既有全局，不加新注入面——与
  zapc 接管 observ 默认、otelc 装配全局 provider 的既有哲学一致；
  promc 的 handler 与 registry 经 `WithProm(PromProvider)` 注入：
  定义在 httpserver 的窄接口（结构化类型），`*promc.Prom` 天然满足，
  **httpserver 不 import promc**——ADR-0001 组件零依赖不破，"强绑定"
  由装配点接线表达。
- **指标直连 prometheus 原生 API，不走 observ.Meter**：`observ.Meter`
  契约（`NewCounter(name, help)`）没有 label 维度，无法表达
  method/pattern/status_class 切片。基础设施组件用原生 CounterVec/
  HistogramVec（promc 自己也直连 prometheus），业务埋点仍走
  observ.Meter——两类调用面分立，不是 observ 的绕过。pattern 标签用
  ServeMux 匹配模板（Go 1.23+ `r.Pattern`）而非原始 path，基数受路由
  数约束。
- **可信端点判定复用 trusted_proxies，Link 语义由 otelhttp 原生承担**：
  RemoteAddr ∈ trusted_proxies → 继承 traceparent 为父 span（内部链路
  串联）；否则走 otelhttp `WithPublicEndpointFn` 路径——`WithNewRoot`
  新建 root span 且把外部 traceparent 转为 `trace.WithLinks`（设计时
  以为要自写提取层，核实 otelhttp ≥0.60 源码后确认原生支持）。两份
  信任配置（代理剥离与链路信任）共用一份网段表，不另开配置项。
- **配套修正（实现期由离线测试捕获的真问题）**：otel ≥1.33 的全局
  propagator 缺省是 **noop**——otelc 不显式 `SetTextMapPropagator`
  的话，traceparent 提取（服务间串联）会静默失效。otelc 作为 OTel
  全局装配方，New 时一并安装 W3C（TraceContext + Baggage）复合传播器。
- **request_id 的中立键（pkg/ctxkey）**："业务日志与链路日志 RequestID
  统一"要求 otelc 的日志装饰读 httpserver 注入的 ctx 值——键定义在
  任何一方都会造成组件间 import 依赖，归中立的 pkg（零第三方依赖），
  双方各自 import。
- **span 命名回填位置**：otelhttp 在其 ServeHTTP 返回时 `defer
  span.End()`，End 后 `SetName`/`SetAttributes` 被丢弃——改名必须在
  otelhttp 内层做：RequestID 中间件（链上位于 tracing 内侧）在 mux
  匹配后、span 未结束时回填 `GET /api/{id}` 与 `http.route`。

## Considered Options

- **promc 保持独立 :9090 端口**——拒绝：多一个暴露面、两份摘流逻辑；
  handler 本就是为挂载预留的（`MetricsHandler` 注释原话）。
- **promc 包级全局 handler 注册（httpserver 读全局）**——拒绝：又一份
  隐藏全局态，与"全局仅限 zap/otel/meter 三件"的既定边界漂移；
  `WithProm` 接口注入是唯一既显式又能让 httpserver 统一管跳过清单的
  方案。
- **扩展 observ.Meter 加 label 维度**——拒绝：改外部契约库，波及面大；
  契约的极小接口是刻意的（ADR-0003 同源立场），基础设施直连原生 API
  代价为零。
- **自写可信判定 + span 提取层（不用 otelhttp）**——最初预期必要，
  核实 otelhttp v0.71 源码（`PublicEndpointFn` 收 `*http.Request`、
  public 路径原生 `WithNewRoot + WithLinks`）后放弃：~80 行自维护代码
  换零收益。
- **HTTP+HTTPS 双监听 / TLS 证书热轮换**——拒绝：单监听切换覆盖模板
  场景（k8s 终止 TLS 是常态）；证书轮换靠重启或重新部署。
- **runner 停机预算顺带放开（5s/10s → 15s/30s 缺省 + SetBudgets）**——
  httpserver 是模板第一个需要真实排空时间的组件，预算不放开则优雅
  停机形同虚设；改动极小（常量变字段 + 校验）。
