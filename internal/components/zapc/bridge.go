// bridge.go 是 slog → zap 的稳定桥：把 zap 后端装成 slog.Handler，经
// slog.SetDefault 接管全局默认日志器。级别映射、属性编码与 ctx 注入
// 语义：
//
//	级别：slog.LevelDebug/Info/Warn/Error ↔ 同名 zapcore 级别，
//	中间自定义级别就近向下（更严重）取整。
//	属性：slog.Attr 逐个显式转 zap Field，键名原样透传；LogValuer 在
//	编码前解析（对齐 slog handler 语义）；除 KindAny 兜底外无反射；
//	组属性以点号前缀展平，空名组内联。
//	ctx：WithCtxAttrsBridge 注入提取器时，每次写入前从 ctx 提取属性（链路
//	注入 trace_id/span_id/request_id 等）追加在调用方属性之后。注入做
//	在桥内部——调用面无装饰层；提取器须并发安全且快速返回。
//
// 动态实例：NewBridge 包装"取当前实例的函数"而非固定实例——热更重建
// 换新后桥自动跟随，桥身份恒定、可安全长持（slog.SetDefault 在进程内
// 一次性完成）。caller 定位取 slog Record 自带的 PC（slog 捕获的即调用
// 方现场），不经 zap 的栈回溯，故无需 skip 烘焙、层数恒定。
package zapc

import (
	"context"
	"log/slog"
	"runtime"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// BridgeOption 桥的构造选项。
type BridgeOption func(*bridgeConfig)

type bridgeConfig struct {
	ctxAttrs func(context.Context) []slog.Attr
}

// WithCtxAttrs 注入 ctx 属性提取器：每次写入前调用，返回的属性追加在
// 调用方属性之后（zap 对同名键双写不覆盖——调用方显式传过的键应避免在
// 提取器中重复产出）。nil 返回值合法（零属性差异）。只影响稳定桥，
// 不影响 zap.L() 直调与 Current 调用面。
func WithCtxAttrsBridge(fn func(context.Context) []slog.Attr) BridgeOption {
	return func(c *bridgeConfig) { c.ctxAttrs = fn }
}

// NewBridge 返回包装动态 zap 实例的 slog.Handler：current 每次写入取
// 当前生效实例（须并发安全；热更重建换新后自动跟随）。经 slog.SetDefault
// 安装后无需随实例重建重装。current 返回 nil 时静默丢弃（零值防御）。
func NewBridge(current func() *zap.Logger, opts ...BridgeOption) slog.Handler {
	var c bridgeConfig
	for _, o := range opts {
		o(&c)
	}
	return &bridge{current: current, ctxAttrs: c.ctxAttrs}
}

// bridge 以指针形态存入接口（禁止拷贝）。
type bridge struct {
	current  func() *zap.Logger
	ctxAttrs func(context.Context) []slog.Attr
	group    string      // WithGroup 累积的键前缀（"" = 无）
	bound    []zap.Field // WithAttrs 编码后的绑定字段（nil = 未绑定）
}

func mapLevel(l slog.Level) zapcore.Level {
	switch {
	case l <= slog.LevelDebug:
		return zapcore.DebugLevel
	case l <= slog.LevelInfo:
		return zapcore.InfoLevel
	case l <= slog.LevelWarn:
		return zapcore.WarnLevel
	default:
		return zapcore.ErrorLevel
	}
}

// Enabled 经当前实例的 core 判断（桥不做自己的级别门控，禁用判定与
// 输出面恒一致）。
func (b *bridge) Enabled(_ context.Context, level slog.Level) bool {
	inst := b.current()
	return inst != nil && inst.Core().Enabled(mapLevel(level))
}

// WithAttrs 绑定属性：每次写入顺序为"绑定 → 调用时 → ctx 提取"，可
// 叠加（先绑定者靠前）。绑定字段此刻求值编码一次（对齐 zap.With：
// LogValuer 等在绑定点求值），Handle 中统一追加；返回的仍是动态桥，
// 热更换新自动跟随。
func (b *bridge) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return b
	}
	bound := make([]zap.Field, 0, len(b.bound)+len(attrs))
	bound = append(bound, b.bound...)
	for _, a := range attrs {
		bound = appendAttr(bound, a, b.group)
	}
	return &bridge{current: b.current, ctxAttrs: b.ctxAttrs, group: b.group, bound: bound}
}

// WithGroup 以点号前缀展平（zap 无组概念；对齐 slog 组嵌套的扁平化
// 惯例），后续绑定与调用时属性均落该前缀下。
func (b *bridge) WithGroup(name string) slog.Handler {
	if name == "" {
		return b
	}
	group := name
	if b.group != "" {
		group = b.group + "." + name
	}
	return &bridge{current: b.current, ctxAttrs: b.ctxAttrs, group: group, bound: b.bound}
}

func (b *bridge) Handle(ctx context.Context, r slog.Record) error {
	inst := b.current()
	if inst == nil {
		return nil
	}
	lvl := mapLevel(r.Level)
	if !inst.Core().Enabled(lvl) {
		return nil
	}
	// 绑定字段在此统一追加（不另派生 inst.With：两处都焊会重复输出）。
	fields := make([]zap.Field, 0, len(b.bound)+r.NumAttrs())
	fields = append(fields, b.bound...)
	r.Attrs(func(a slog.Attr) bool {
		fields = appendAttr(fields, a, b.group)
		return true
	})
	if b.ctxAttrs != nil {
		for _, a := range b.ctxAttrs(ctx) {
			fields = appendAttr(fields, a, b.group)
		}
	}
	ent := zapcore.Entry{Level: lvl, Time: r.Time, LoggerName: inst.Name(), Message: r.Message}
	if r.PC != 0 {
		fr, _ := runtime.CallersFrames([]uintptr{r.PC}).Next()
		ent.Caller = zapcore.EntryCaller{Defined: fr.File != "", PC: r.PC, File: fr.File, Line: fr.Line}
	}
	if ce := inst.Core().Check(ent, nil); ce != nil {
		ce.Write(fields...)
	}
	return nil
}

// appendAttr 把 slog.Attr 追加编码为 zap Field（prefix 非空时以点号
// 前缀展平）。对齐 slog handler 语义：编码前解析 LogValuer（解析可能
// 改变 Kind，包括解析为组值）。
func appendAttr(fields []zap.Field, a slog.Attr, prefix string) []zap.Field {
	a.Value = a.Value.Resolve()
	key := a.Key
	if prefix != "" {
		key = prefix + "." + key
	}
	switch a.Value.Kind() {
	case slog.KindString:
		return append(fields, zap.String(key, a.Value.String()))
	case slog.KindInt64:
		return append(fields, zap.Int64(key, a.Value.Int64()))
	case slog.KindUint64:
		return append(fields, zap.Uint64(key, a.Value.Uint64()))
	case slog.KindFloat64:
		return append(fields, zap.Float64(key, a.Value.Float64()))
	case slog.KindBool:
		return append(fields, zap.Bool(key, a.Value.Bool()))
	case slog.KindDuration:
		return append(fields, zap.Duration(key, a.Value.Duration()))
	case slog.KindTime:
		return append(fields, zap.Time(key, a.Value.Time()))
	case slog.KindGroup:
		// 空名组内联（对齐 slog）：子属性沿用父前缀，避免产生
		// "http..key" 双点号键。
		child := key
		if a.Key == "" {
			child = prefix
		}
		for _, ga := range a.Value.Group() {
			fields = appendAttr(fields, ga, child)
		}
		return fields
	default: // 含 KindAny：反射兜底
		return append(fields, zap.Any(key, a.Value.Any()))
	}
}
