package middleware

import (
	"context"
	"io"
	"net/http"

	"go.opentelemetry.io/otel/trace"
)

// reqState 是单请求的中间件共享状态：最外层（Recovery）创建并挂 ctx，
// 内层（RequestID）填写，外层（访问日志 / panic 日志）消费。单请求内
// 串行访问，无并发竞争。
type reqState struct {
	requestID string     // RequestID（RequestID 中间件回填）
	traceID   string     // TraceID（同上；无有效 span 时留空）
	pattern   string     // 匹配的路由模板（同上；未匹配为空）
	span      trace.Span // 请求 span（路由匹配后改名用；nil 安全）
}

// stateCtxKey 是 reqState 的 ctx 键。
type stateCtxKey struct{}

func stateFromContext(ctx context.Context) *reqState {
	st, _ := ctx.Value(stateCtxKey{}).(*reqState)
	return st
}

// respRecorder 记录状态码与写出字节数（访问日志与 Recovery 判定响应
// 是否开头）。Unwrap 让 http.ResponseController（及现代 pprof/SSE 路径）
// 能穿透到底层 ResponseWriter；另直挂 Flush 兼容直接断言 http.Flusher
// 的业务代码。
type respRecorder struct {
	http.ResponseWriter
	status int   // 首次 WriteHeader 的状态码（未显式写头的写体按 200）
	bytes  int64 // 累计写出字节
	wrote  bool  // 是否已开头（写头或写体）
}

func (w *respRecorder) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *respRecorder) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *respRecorder) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *respRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// countingReader 记录实际读取的请求体字节数（ContentLength 对 chunked
// 是 -1，不可信——以实测为准）。
type countingReader struct {
	rc    io.ReadCloser
	count int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	c.count += int64(n)
	return n, err
}

func (c *countingReader) Close() error { return c.rc.Close() }
