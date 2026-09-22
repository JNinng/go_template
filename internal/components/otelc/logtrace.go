package otelc

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/jninng/observ"
	"go.opentelemetry.io/otel/trace"

	"go_template/pkg/ctxkey"
)

// installLogTrace 以装饰型 observ.Logger 注入链路属性：Log 前从 ctx
// 读取有效 span，附加 trace_id/span_id 后委托后端；ctx 携带 request_id
// （pkg/ctxkey，httpserver 注入）时一并附加。此后运行期动态读
// DefaultLogger() 的调用全部自动携带链路信息；构造期快照持有者（早于
// 本组件拿到 logger 的组件）保持旧面。幂等：默认已是本装饰时跳过
// （重复构造不叠装饰）。
//
// 注入做在 observ 边界而非 slog handler 层：handler 级包装须接管
// slog.SetDefault，而 slog 的内部 defaultHandler 写路径经 stdlib log 桥，
// SetDefault 会把该桥重定向回新默认形成自环（首条日志即在 log.Logger
// 的锁上死锁）；observ 边界装饰零全局接管，且与任意后端（slog、zaplog
// 桥）正交。
func installLogTrace() {
	cur := observ.DefaultLogger()
	if _, ok := cur.(*traceLogger); ok {
		return
	}
	observ.SetDefaultLogger(newTraceLogger(cur))
}

// traceLogger 是链路属性注入装饰器：ctx 携带有效 span 时附加
// trace_id/span_id，携带 request_id 时附加 request_id（业务日志与链路
// 日志由此对齐）；无 span 或 span 无效时原样委托（零属性差异）。
// Enabled 纯透传。后端原子持有——Rebind 换绑不与在途调用竞争。
type traceLogger struct{ next atomic.Pointer[observ.Logger] }

func newTraceLogger(next observ.Logger) *traceLogger {
	t := &traceLogger{}
	t.next.Store(&next)
	return t
}

// Rebind 原子换绑委托后端。后端接管组件（zapc）换新 observ 默认时经
// 结构化协议探测并调用本方法——装饰原地存活、无需重装，zapc 的接管与
// 热更重建由此都不断链路注入。
func (t *traceLogger) Rebind(next observ.Logger) { t.next.Store(&next) }

// current 返回委托后端（零值防御：未初始化时 Noop）。
func (t *traceLogger) current() observ.Logger {
	if p := t.next.Load(); p != nil {
		return *p
	}
	return observ.NoopLogger
}

func (t *traceLogger) Enabled(ctx context.Context, level slog.Level) bool {
	return t.current().Enabled(ctx, level)
}

func (t *traceLogger) Log(ctx context.Context, level slog.Level, msg string, attrs ...slog.Attr) {
	if id := ctxkey.RequestID(ctx); id != "" {
		attrs = append(attrs, slog.String("request_id", id))
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		// attrs 变参切片按 Go 惯例调用后不得再被调用方复用；当前
		// observ 实现均同步消费，就地追加安全，追加后直传后端。
		attrs = append(attrs,
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	t.current().Log(ctx, level, msg, attrs...)
}
