// Package ctxlog 承载请求作用域日志记录器的 ctx 通道：RequestID 层把
// 预绑定链路字段（request_id/trace_id/span_id）的 *zap.Logger 挂进
// ctx，业务 handler 经根包 LoggerFrom 取用。独立子包避免根包与
// internal/middleware 的 import 环（键被两侧消费，根包只转发访问器）。
package ctxlog

import (
	"context"
	"go.uber.org/zap"
)

// loggerCtxKey 是请求日志记录器的 ctx 键。
type loggerCtxKey struct{}

// With 把请求日志记录器挂进 ctx。
func With(ctx context.Context, l *zap.Logger) context.Context {
	return context.WithValue(ctx, loggerCtxKey{}, l)
}

// From 取请求日志记录器；未挂载（非 httpserver 请求语境）时回落 zap
// 全局——调用方无需判空，缺富化字段即"不在请求链路内"的信号。
func From(ctx context.Context) *zap.Logger {
	if l, ok := ctx.Value(loggerCtxKey{}).(*zap.Logger); ok && l != nil {
		return l
	}
	return zap.L()
}
