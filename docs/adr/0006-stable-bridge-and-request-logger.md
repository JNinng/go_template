# 稳定桥取代 Rebind 协议：caller 恒定、链路注入沉入适配层、请求日志独立

zapc / otelc / httpserver 三组件的日志组合在运行中发现系统性缺陷：observ
默认日志器 caller 错位、zap.L() 直调路径缺链路注入、访问日志与业务日志
混流。本 ADR 记录重设计的决策与落选项（ADR-0005 的后续演进）。

## 背景

原形态：zapc 接管 observ 默认时构造 zaplog 桥（`AddCallerSkip(1)` 只补偿
zaplog 适配帧），并以鸭子类型探测 `Rebind(observ.Logger)` 协议——默认
日志器是装饰器（otelc 链路注入的 traceLogger）时原地重绑而非整体替换。
离线探针证实：装饰层叠加后 caller 恒指装饰器委托行而非用户代码行；且
skip 在桥构造时烘焙、层数在装配时决定，两者由不同方在不同时刻决定，
任何新增装饰层都会再错一位。

## 决策

- **稳定桥取代 Rebind 协议**：桥包装 kit（`zaplog.NewDynamic(kit.CurrentSkip1)`）
  而非某一次构建的实例——热更重建只换 kit 内实例、桥自动跟随，
  `SetDefaultLogger` 进程内一次性完成。Rebind 探测、接管回调、装饰层
  "必须实现 Rebind 才能存活"的隐性契约整体删除；桥身份恒定使 skip 只需
  在实例侧烘焙一次（恒 +1，补偿 zaplog 适配帧），调用面无装饰层。
- **链路注入沉入 zaplog 适配层（`WithCtxAttrs`）**：traceLogger 装饰在
  zapc 标准形态下退役，注入改为桥的构建参数（装配点传
  `zapc.WithCtxAttrs(otelc.CtxLogAttrs)`，zapc 不 import otel、otelc 不
  import 桥，组合由装配点接线表达）。otelc 保留装饰路径作为 slog 缺省
  后端的兜底（未接 zapc 的项目）；handler 级包装依旧不可行——slog
  SetDefault 与 stdlib log 桥的自环死锁（见 logtrace 注释）是硬边界。
  装配序约束不变：otelc 先、zapc 后（无双份注入）。
- **httpserver 请求日志独立记录器（`WithAccessLogger`）**：访问日志与
  panic 日志走注入的 getter（每请求取当前实例，热更自动跟随；未注入
  回落 `zap.L()` 保持历史行为；ctx 日志见下条，不经此通道）。独立实例由装配点从 zapc
  节派生——独立的 zapc 实例（等级独立门控、热更跟随），仅两处派生：
  `path` 固定 zapc.path 同目录 `req.log`（path 为空兜底 `log/req.log`，
  访问日志落盘不随控制台形态缩水）、caller 关闭（`WithEncoderConfig`
  置空 `CallerKey`——访问日志 caller 恒为中间件同一行，无定位价值）。
  派生归装配点胶水而非 httpserver 配置面：httpserver 保持零 zapc 依赖，
  日志分文件的策略属部署形态。
- **ctx 日志通道（`LoggerFrom`）走 zap 全局而非注入记录器**：
  RequestID 层把预绑定 request_id/trace_id/span_id 的记录器挂进 ctx，
  业务 handler 深处免逐层穿字段；记录器由 zap 全局逐请求派生（zap
  `With`，热更重建自动跟随）——业务日志归应用流，req.log 只放访问
  记录（若走注入 getter，业务日志会混入请求日志流并受其等级门控）。
  键归 internal/ctxlog（根包与 middleware 双方消费，放任一方都成
  import 环；根包转发访问器）。直调 `zap.L()` 与本通道同源但无预绑定
  字段——请求内日志一律走 `LoggerFrom`。

## Considered Options

- **协议化 skip 补偿（装饰器上报封装深度，桥按深度校准）**——拒绝：
  保留 Rebind 的探测与隐式契约，只是把错位从"恒错一位"改为"按上报补偿"，
  装饰层仍须维护协议、新增封装形态仍可错位；稳定桥从根上消除层数变量。
- **slog handler 层注入（两后端统一走 handler 读 ctx）**——拒绝：slog
  `SetDefault` 与 stdlib log 桥自环死锁（包装旧 defaultHandler 的路径
  首条日志即在 log.Logger 锁上死锁），硬边界不可绕行。
- **请求日志并入 zapc 组件（`New` 顺带 spawn req kit）**——拒绝：
  `req.log` 命名与派生策略是 httpserver 的部署关切，zapc 不应知晓；
  独立实例由装配点 `NewLogger` 派生，zapc 只需提供组合原语
  （`WithWatch` / `WithEncoderConfig` / `Current` 方法值）。
- **请求日志不做热更（一次性 `Build` 裸实例）**——拒绝：format/轮转/
  等级与 zapc 节同源，热更是模板既有语义；派生 Watch 转发的成本一行
  闭包，收敛判断在 zapc 侧照常生效。
- **ctx 日志走注入的请求记录器（初版形态）**——拒绝：业务日志混入
  req.log（访问流排障口径被业务日志稀释）且受请求日志等级门控；ctx
  记录器改为 zap 全局逐请求派生，业务日志归应用流，两流按排障口径
  分立。
