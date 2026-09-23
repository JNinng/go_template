package otelc

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/jninng/observ"
	"go.opentelemetry.io/otel/trace"

	"go_template/pkg/ctxkey"
)

// CtxLogAttrs 从 ctx 提取链路日志属性：ctx 携带 request_id（pkg/ctxkey，
// httpserver 注入）时附加 request_id，携带有效 span 时附加 trace_id/
// span_id；两者皆无时返回 nil（零属性差异）。并发安全、快速返回。
//
// 消费方有二：装配点经 zapc.WithCtxAttrs 把本函数注入 zaplog 适配层
// （链路注入沉入适配层，调用面无装饰层，caller 定位不随封装漂移——
// zapc 后端的标准形态）；以及本组件的 traceLogger（slog 缺省后端的
// 兜底注入，未接 zapc 的项目经它保持链路日志对齐）。
func CtxLogAttrs(ctx context.Context) []slog.Attr {
	var attrs []slog.Attr
	if id := ctxkey.RequestID(ctx); id != "" {
		attrs = append(attrs, slog.String("request_id", id))
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		attrs = append(attrs,
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return attrs
}

// installLogTrace 以装饰型 observ.Logger 注入链路属性：Log 前经
// CtxLogAttrs 从 ctx 附加链路属性后委托后端。幂等：默认已是本装饰时
// 跳过（重复构造不叠装饰）。
//
// 定位是 slog 缺省后端的兜底注入——未接 zapc 的项目经它保持业务日志与
// 链路日志对齐。接了 zapc 时接管会整体替换本装饰（链路注入由装配点
// WithCtxAttrs(otelc.CtxLogAttrs) 在适配层完成），装配序恒为
// otelc 先、zapc 后（书写顺序即依赖顺序），不存在双份注入。
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

// traceLogger 是链路属性注入装饰器：ctx 携带链路信息时附加后委托后端。
// Enabled 纯透传。后端原子持有，构造后不再更换；接 zapc 的项目经
// 接管整体替换默认日志器，本装饰随之让位（见 installLogTrace）。
type traceLogger struct{ next atomic.Pointer[observ.Logger] }

func newTraceLogger(next observ.Logger) *traceLogger {
	t := &traceLogger{}
	t.next.Store(&next)
	return t
}

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
	// attrs 变参切片按 Go 惯例调用后不得再被调用方复用；当前 observ
	// 实现均同步消费，就地追加安全，追加后直传后端。
	attrs = append(attrs, CtxLogAttrs(ctx)...)
	t.current().Log(ctx, level, msg, attrs...)
}
