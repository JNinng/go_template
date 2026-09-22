// Package ctxkey 提供跨组件共享的 context 键。值的消费者与生产者分属
// 不同组件时，键不能定义在任何一方（否则组件间产生 import 依赖），
// 归中立的 pkg 定义、双方各自 import——本包因此零第三方依赖、恒稳定。
//
// 现有键：request_id（httpserver 注入、otelc 日志装饰消费）。
package ctxkey

import "context"

// requestIDKey 是 request_id 的 context 键（结构体类型，零碰撞）。
type requestIDKey struct{}

// WithRequestID 返回携带 request_id 的 ctx（空值也如实携带）。
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID 取出 ctx 中的 request_id；未携带返回空串。
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}
