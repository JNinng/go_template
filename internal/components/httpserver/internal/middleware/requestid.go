package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"

	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.27.0"
	"go.opentelemetry.io/otel/trace"

	"go_template/pkg/ctxkey"
)

// requestIDHeader 是 RequestID 的请求/响应头名。
const requestIDHeader = "X-Request-ID"

// traceIDHeader 是 TraceID 的响应头名。
// 值恒为本服务端 span 的 TraceID（可信继承或新 root 同口径）——外部
// 传入的 traceparent 不回显，防伪造值借响应头回流。
const traceIDHeader = "X-Trace-ID"

// requestIDRe 是外部 RequestID 的接受标准：字符白名单 + 64 上限——
// 拒绝日志注入与超长攻击面；不合规值静默忽略（可观测设施不鉴权，
// 宁可降级到兜底值，不断流）。
var requestIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// requestID 解析 RequestID 并保证恒有值：
//
//   - 请求头携带合法 X-Request-ID → 原样注入 ctx 并回写响应头；
//   - 缺失/非法 → 静默忽略，取 OTel TraceID 兜底（本层位于 tracing 内层，
//     span 已生成）；span 无效（无 TracerProvider）时 crypto/rand 32 hex
//     兜底——业务日志与链路日志的 RequestID 由此统一。
//
// 响应头与 RequestID 同步写 X-Trace-ID（span 有效时）——响应、日志、
// 链路三方凭同一 TraceID 互查。同时把 request_id / trace_id / span /
// 路由模板回填进请求共享状态（reqState），供外层访问日志与 Recovery
// 的 panic 日志消费。span 改名须在本层做：本层以 WithContext 复制请求
// 对象（mux 把 r.Pattern 回填在副本上，再外层已不可见），且本层返回时
// span 尚未 End（otelhttp 的 defer End 在其返回时才触发），此刻
// SetName/属性仍生效。
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sc := trace.SpanContextFromContext(r.Context())
		id := r.Header.Get(requestIDHeader)
		if !requestIDRe.MatchString(id) {
			id = ""
		}
		if id == "" {
			if sc.IsValid() {
				id = sc.TraceID().String()
			} else {
				id = randomRequestID()
			}
		}
		w.Header().Set(requestIDHeader, id)
		if sc.IsValid() {
			w.Header().Set(traceIDHeader, sc.TraceID().String())
		}

		st := stateFromContext(r.Context())
		if st != nil {
			st.requestID = id
			if sc.IsValid() {
				st.traceID = sc.TraceID().String()
			}
			if sp := trace.SpanFromContext(r.Context()); sp != nil {
				st.span = sp
			}
		}
		r = r.WithContext(ctxkey.WithRequestID(r.Context(), id))
		next.ServeHTTP(w, r)
		if st != nil {
			if p := r.Pattern; p != "" {
				st.pattern = p
				if st.span != nil {
					st.span.SetName(r.Method + " " + p)
					st.span.SetAttributes(semconvHTTPRoute(p))
				}
			}
		}
	})
}

// randomRequestID 是无链路时的兜底（crypto/rand 16 字节 hex）；随机源
// 失败返回固定串——恒有值优先于唯一性。
func randomRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "untraced"
	}
	return hex.EncodeToString(b[:])
}

// semconvHTTPRoute 是 http.route 属性（OTel semantic conventions）。
// 经局部函数包一层：semconv 版本升级时只改一处。
func semconvHTTPRoute(pattern string) attribute.KeyValue {
	return semconv.HTTPRoute(pattern)
}
