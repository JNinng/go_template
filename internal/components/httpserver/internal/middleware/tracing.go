package middleware

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"go_template/internal/components/httpserver/internal/trust"
)

// tracing 经 otelhttp 为每个请求生成服务端 span，并按可信代理网段
// （Deps.Proxies）做可信端点判定：
//
//   - RemoteAddr 可信（内网/网关）→ 继承请求头 traceparent 为父 span，
//     内部链路上下游串联；
//   - 不可信（外部直连）→ PublicEndpoint 路径：otelhttp 以 WithNewRoot
//     起全新 root span，并把外部 traceparent 转为 Link（不继承但保留
//     关联）——外部伪造链路无法污染内部拓扑，跨系统排查仍可循 Link 回溯。
//
// span 缺省名为 method（"GET"）；路由模板由内层 RequestID 在 mux 匹配后
// 回填（GET /api/{id} + http.route）。TracerProvider 用全局（otelc 构造
// 时装载；组件删除后 otel 全局退化为 no-op，链路静默消失不报错）。
func tracing(d Deps, next http.Handler) http.Handler {
	return otelhttp.NewHandler(next, "",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method
		}),
		otelhttp.WithPublicEndpointFn(func(r *http.Request) bool {
			ip, ok := trust.RemoteIP(r)
			if !ok {
				return true // 来源无法判定：按不可信处理（保守）
			}
			return !d.Proxies().Contains(ip)
		}),
	)
}
