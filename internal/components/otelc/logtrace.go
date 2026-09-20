package otelc

import (
	"context"
	"log/slog"

	"github.com/jninng/observ"
	"go.opentelemetry.io/otel/trace"
)

// installLogTrace 以装饰型 observ.Logger 注入链路属性：Log 前从 ctx
// 读取有效 span，附加 trace_id/span_id 后委托原实现。此后运行期动态读
// DefaultLogger() 的调用全部自动携带链路信息；构造期快照持有者（早于
// 本组件拿到 logger 的组件）保持旧面，接线顺序见 README。
//
// 注入做在 observ 边界而非 slog handler 层：handler 级包装须接管
// slog.SetDefault，而 slog 的内部 defaultHandler 写路径经 stdlib log 桥，
// SetDefault 会把该桥重定向回新默认形成自环（首条日志即在 log.Logger
// 的锁上死锁）；observ 边界装饰零全局接管，且与任意后端（slog、zaplog
// 桥）正交。
func installLogTrace() {
	observ.SetDefaultLogger(traceLogger{next: observ.DefaultLogger()})
}

// traceLogger 是链路属性注入装饰器：ctx 携带有效 span 时附加
// trace_id/span_id；无 span 或 span 无效时原样委托（零属性差异）。
// Enabled 纯透传。
type traceLogger struct{ next observ.Logger }

func (l traceLogger) Enabled(ctx context.Context, level slog.Level) bool {
	return l.next.Enabled(ctx, level)
}

func (l traceLogger) Log(ctx context.Context, level slog.Level, msg string, attrs ...slog.Attr) {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		// 契约：attrs 切片传入后归实现所有，就地追加安全；
		// 装饰后的切片作为新调用方传给 next，此后不再触碰。
		attrs = append(attrs,
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	l.next.Log(ctx, level, msg, attrs...)
}
