# 日志单一调用面 observ

模板代码严格经 observ.Logger 记日志（`Enabled(slog.Level) bool` / `Log(level, msg string, attrs ...slog.Attr)`）。装配点设 observ 默认后端：缺省实现零配置可用（基于 stdlib 构建）；业务换 zap 时装配点一处换向——`observ.SetDefaultLogger(zaplog.New(z))`——业务自身代码直调 zap，不经 observ、不经任何中间层，高频路径零额外开销。组件不强制：原生组件推荐走 observ 约定，第三方组件按其日志面经适配层桥接。

配套规则：

- **持有方式分两级**：模板包不持有 logger 字段、调用点动态读 `observ.DefaultLogger()`（atomic 读）——config 包的构造早于日志装配（`log:` 节在配置里，先有配置后有后端），构造期快照会永久固定在 Noop；动态读同时让换后端对已构造的模板设施立即生效。原生组件按 observ 规范构造期快照即可——其构造发生在装配点、晚于后端设置，快照即正确后端，模板无须向组件传递任何 logger；第三方组件不做此要求。
- `log:` 节仅 level / format / output 三字段，level 唯一热更（作用于后端级别，调用面无感）；不做文件轮转。
- 第三方库的日志面由适配层桥接：收 `*slog.Logger` 的传 `slog.Default()`；自有 logger 接口的用 observ.Logger 实现之；收具体后端（zap 等）的直接传后端实例，不绕 observ。

## Considered Options

- **模板自封装日志包**（统一签名自有包，如 `Info(msg string, fields ...Field)`）——拒绝：复制体里多一个需要维护的自有包；换库 = 重写该包 + 手改全部调用点。
- **双调用面：模板走 slog 包级、组件走 observ，装配点汇聚**——拒绝：换后端需同时替换两个漏斗，存在"漏替一个"的失败模式；模板日志能否跟随业务后端，隐含依赖"slog.Default 可被重指"这一前提。单一 observ 调用面把模板日志的换向收敛为 observ 默认一处，机制唯一。
- **全员直接 slog（组件也调 slog）**——拒绝：组件被绑死 slog 生态；observ 接口 + Noop 回落是组件侧的正确抽象粒度，zap 侧有现成 adapter。
