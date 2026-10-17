package otelc

import (
	"context"
	"log/slog"
	"reflect"

	"go.opentelemetry.io/otel/trace"

	"go_template/pkg/ctxkey"
)

// isStdlibDefaultHandler 判定 handler 是否为 slog 内部缺省 handler（含
// 其派生形态）。该 handler 的写路径经 stdlib log 桥，包裹它再
// slog.SetDefault 会把 log 桥重定向回装饰器形成自环（首条日志即在
// log.Logger 的锁上死锁），故安装时跳过。按类型而非接口值比较：
// `slog.Default().With(...)` 派生出的仍是同类型的 defaultHandler（新
// 指针），只比初始快照会漏判；按 `log/slog` 包路径 + 类型名判定则对
// init 顺序与派生都稳健。宿主先装真实后端（或引导层的 discard 默认）
// 后该类型不可达，此守卫只覆盖"未走引导直接构造本组件"的情形。
func isStdlibDefaultHandler(h slog.Handler) bool {
	if h == nil {
		return false
	}
	t := reflect.TypeOf(h)
	if t.Kind() == reflect.Ptr {
		t = t.Elem() // defaultHandler 以 *defaultHandler 存储；指针类型无名字
	}
	return t.PkgPath() == "log/slog" && t.Name() == "defaultHandler"
}

// CtxLogAttrs 从 ctx 提取链路日志属性：ctx 携带 request_id（pkg/ctxkey，
// httpserver 注入）时附加 request_id，携带有效 span 时附加 trace_id/
// span_id；两者皆无时返回 nil（零属性差异）。并发安全、快速返回。
//
// 消费方有二：装配点经 zapc.WithCtxAttrs 把本函数注入 zapc 稳定桥
// （链路注入沉入桥内部，调用面无装饰层，caller 定位不随封装漂移——
// zapc 后端的标准形态）；以及本组件的 traceHandler（slog 缺省后端的
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

// installLogTrace 以装饰型 slog.Handler 注入链路属性：Handle 前经
// CtxLogAttrs 从 ctx 附加链路属性后委托后端。幂等：默认已是本装饰时
// 跳过（重复构造不叠装饰）。
//
// 定位是 slog 缺省后端的兜底注入——未接 zapc 的项目经它保持业务日志与
// 链路日志对齐。接了 zapc 时接管会整体替换本装饰（链路注入由装配点
// WithCtxAttrs(otelc.CtxLogAttrs) 在 zapc 桥内完成），装配序恒为
// otelc 先、zapc 后（书写顺序即依赖顺序），不存在双份注入。
//
// 注入做在 slog handler 层的前提是缺省后端非 stdlib 内部缺省——模板
// 引导（app.Run 第 0 步）先装 discard 默认，setupLogging 再换真实后端，
// 两者均可安全包裹；未走引导的宿主跳过注入（见 isStdlibDefaultHandler）。
func installLogTrace() {
	cur := slog.Default()
	h := cur.Handler()
	if _, ok := h.(*traceHandler); ok || isStdlibDefaultHandler(h) {
		return
	}
	slog.SetDefault(slog.New(&traceHandler{next: h}))
}

// traceHandler 是链路属性注入装饰器：ctx 携带链路信息时附加后委托后端。
// Enabled 纯透传；WithAttrs / WithGroup 逐层包裹，装饰不因派生而脱落。
// 后端在安装期固定，接了 zapc 的项目经接管整体替换 slog 默认，本装饰
// 随之让位（见 installLogTrace）。
type traceHandler struct{ next slog.Handler }

func (h *traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *traceHandler) Handle(ctx context.Context, r slog.Record) error {
	// Record 为值语义，AddAttrs 就地追加安全，追加后直传后端。
	if attrs := CtxLogAttrs(ctx); len(attrs) > 0 {
		r.AddAttrs(attrs...)
	}
	return h.next.Handle(ctx, r)
}

func (h *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceHandler{next: h.next.WithAttrs(attrs)}
}

func (h *traceHandler) WithGroup(name string) slog.Handler {
	return &traceHandler{next: h.next.WithGroup(name)}
}
