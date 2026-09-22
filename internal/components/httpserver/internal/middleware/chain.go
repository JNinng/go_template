// Package middleware 实现 httpserver 的请求中间件链。组件 internal
// 子包：Chain 由根包在 Start 时组装一次，此后热更只换 Deps 指向的原子
// 态、不换链。
//
// 链序（外→内）：Recovery → CORS → Observability → Tracing → RequestID
// → BodyLimit → 路由表。各自的落位理由见各文件注释。
package middleware

import (
	"net/http"

	"github.com/rs/cors"

	"go_template/internal/components/httpserver/internal/metric"
	"go_template/internal/components/httpserver/internal/trust"
)

// Deps 是中间件链运行所需的组件状态视图：根包 Server 以闭包提供，
// 热更字段（网段表、请求体上限、CORS 实例）经函数调用读到最新值。
// 各函数仅热路径调用，不允许在构造期外快照（热更即失效）。
type Deps struct {
	Proxies     func() trust.Table     // 当前可信代理网段（tracing 判定 + clientIP 剥离）
	InstanceIDs string                 // X-Instance-IDs 响应头值（构造期冻结）
	Metrics     *metric.Metrics        // 预置指标（nil 防御：跳过计数）
	Skip        func(path string) bool // 跳过清单（不记访问日志不计指标；span 照起）
	BodyLimit   func() int64           // 请求体上限（<=0 不限制）
	CORS        func() *cors.Cors      // 当前跨域实例（nil 直通，New 必装故不可达）
}

// Chain 按既定链序组装中间件并包裹 next（路由表）。
func Chain(d Deps, next http.Handler) http.Handler {
	h := bodyLimit(d, next)
	h = requestID(h)
	h = tracing(d, h)
	h = observability(d, h)
	h = corsMiddleware(d, h)
	h = recovery(d, h)
	return h
}
