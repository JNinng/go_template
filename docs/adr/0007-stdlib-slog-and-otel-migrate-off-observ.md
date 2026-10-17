# 日志/指标面收敛 stdlib slog 与 otel：observ 契约库退役

模板的日志与指标调用面从自有契约库 `github.com/jninng/observ` 收敛为生态
标准：日志面 stdlib **slog 包级默认**（`slog.Default()`），指标面 otel
**metric API**（`go.opentelemetry.io/otel/metric`，全局 provider 经
promc 的 `otel.SetMeterProvider` 安装）。observ 及其两个适配器
（adapters/zaplog、adapters/prom）退役，模板依赖清零。

## 背景

observ 是零依赖 Logger/Meter 两方法小接口 + 包级默认 + Noop 回落的契约库
（ADR-0003/0004/0006 的沿革）。其存在理由是"组件侧最小抽象 + 缺省零开销"
。迁移的直接动因有三：

- **日志面与 slog 能力重合**：observ.Logger（Enabled/Log 两方法、ctx 携带、
  slog 类型复用）对 `*slog.Logger` 已无增量——WithAttrs、级别门控、ctx
  透传 slog 原生全有。契约库只剩桥接成本。
- **指标面被基础设施绕行**：observ.Meter 契约无 label 维度（New* 只有
  name/help），httpserver 请求指标被迫直连 prometheus 原生 API
  （ADR-0005）；otel metric API 原生支持 attributes、单位与显式桶、
  ctx 关联（exemplar），契约理由消失。
- **迁移映射可逐项核对**：级别（Debug/Info/Warn/Error 同名对应）、
  仪表类型（Counter/Gauge/Histogram）、构造期快照语义与默认静默均已
  对齐，迁移是语义等价替换而非行为重设计。

## 决策

- **日志面 = `slog.Default()` 包级默认**：模板包动态读（config 构造早于
  日志装配，快照会永久固定）；原生组件 `WithLogger(*slog.Logger)` 注入 +
  构造期快照；源角色组件（setupSources 构造）动态读 + stderr 双通道。
  持有规则与 ADR-0003 同构，收敛点从 observ 默认平移到 slog 默认。
- **默认静默由引导层承接**：`app.Run` 第 0 步 `slog.SetDefault(slog.New(slog.DiscardHandler))`
  ——未配置零输出、零开销，对齐 observ 时代 Noop 缺省语义。该步同时是
  装饰安全的前提：其后任何 `slog.SetDefault` 装饰（otelc 链路注入）包裹
  的都不是 stdlib 内部缺省 handler。
- **自环死锁硬边界的解法更新**：ADR-0004/0006 曾因"slog SetDefault 与
  stdlib log 桥自环死锁"拒绝 handler 层注入、把链路注入做在 observ 边界。
  该拒绝的前置条件是"进程从未设置过具体 handler"；引导层 discard 默认使
  该形态不可达——otelc 链路注入迁为 slog.Handler 装饰（含类型守卫：
  包装对象为 stdlib 内部缺省 handler（含其派生形态）时跳过，覆盖未走
  引导的宿主）。
- **指标面 = otel metric API**：业务埋点经 `otel.GetMeterProvider().Meter(scope)`
  建仪表（未接线 promc 时全局即 noop provider，"缺省不埋点"无需额外导出依赖）；
  promc 改以 otel prometheus 导出器实现 `metric.Reader`——私有 registry、
  go/process collectors、`/metrics` `/health` 暴露形态不变，接线即
  `otel.SetMeterProvider`。仪表绑定创建时刻的 provider，构造期快照语义
  天然对齐（promc 先于业务组件接线的时序约束不变）。仪表名不带
  `_total`/`_seconds`（导出器按类型与单位追加），导出指标名口径不变。
- **zapc 稳定桥自持**：observ/adapters/zaplog 退役后，slog → zap 的
  稳定桥由 zapc 自持（`NewBridge(kit.Current, WithCtxAttrsBridge(...))`
  ，约 180 行）：动态实例跟随、ctx 属性注入、组展平与 LogValuer 解析
  语义自适配器逐条平移；caller 定位改取 slog Record 自带 PC（slog 捕获
  的即调用方现场），zaplog 时代的 skip 烘焙随之删除。
- **httpserver 基础设施指标维持 prometheus 原生 API**：method/pattern/
  status_class 切片在原生 CounterVec/HistogramVec 上表达最直接，经
  `Registry()` 与 otel 业务仪表同场导出（ADR-0005 结论不变，理由更新——
  不再是"契约无 label"而是"切片标签原生形态最直接"）。
- **测试成本接受**：otel 同步仪表接口密封（内嵌未导出标记方法），仪表值
  断言走导出面（/metrics exposition）而非 fake Meter——promc 测试即按此
  形态实现，给测试引入的只有既有 client_golang 依赖。

## Considered Options

- **保留 observ 契约库、仅指标侧迁 otel**——拒绝：日志侧 `*slog.Logger`
  与 observ.Logger 能力重合，多一层桥接、多一个认知对象、多三个依赖
  （observ + 两适配器）而无一收益；"组件侧最小抽象"的诉求 slog 原生
  即满足。
- **zap 桥引入官方 slog handler 而非自持**——拒绝：稳定桥需要"动态实例
  跟随（热更重建只换 kit 内实例）+ ctx 属性注入"的组合语义，外部
  handler 仍需再包一层动态分发，省不了多少代码还多一个依赖；自持桥与
  kit 同包演进、随组件删除即清。
- **promc 维持 client_golang 原生注册、放弃 otel 埋点面**——拒绝：业务
  埋点将各自直连 client_golang，与 otel tracing 的资源/属性体系割裂，
  跨信号关联（exemplar→trace）无从谈起；导出器方案同时保住既有
  `/metrics` 口径与 otel 埋点面。
- **观察契约库另行发布 slog 版**——拒绝：stdlib 本身即是该契约，再发布
  一层只是改名。
