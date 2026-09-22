// Package metric 定义 httpserver 的预置 HTTP 指标。组件 internal 子包：
// 构造与注册由根包 New 驱动（registry 取自 WithProm 注入的 promc）。
//
// 直连 prometheus 原生 API 而非 observ.Meter：后者契约无 label 维度
// （NewCounter(name, help)），无法表达 method/pattern/status_class 切片
// ——基础设施组件用原生 API，业务埋点仍走 observ.Meter（经 promc 安装的
// 默认 Meter）；决策见 ADR-0005。
//
// pattern 标签用路由模板（net/http ServeMux 的匹配模式）而非原始 path
// ——基数受路由数约束；未匹配到路由的请求记 UnmatchedPattern。
package metric

import (
	"github.com/prometheus/client_golang/prometheus"
)

// UnmatchedPattern 是未匹配路由的 pattern 标签值（防基数泄漏）。
const UnmatchedPattern = "unmatched"

// Metrics 是 httpserver 的预置指标集。
type Metrics struct {
	requests *prometheus.CounterVec   // httpserver_requests_total
	duration *prometheus.HistogramVec // httpserver_request_duration_seconds
}

// New 构造并注册到 reg。跳过清单路径（探活、指标、pprof 等）不进 Observe。
func New(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "httpserver_requests_total",
			Help: "HTTP 请求总数，按方法、路由模板与状态码档位（2xx/3xx/4xx/5xx）切片。",
		}, []string{"method", "pattern", "status_class"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "httpserver_request_duration_seconds",
			Help:    "HTTP 请求耗时分布（秒），按方法与路由模板切片。",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "pattern"}),
	}
	reg.MustRegister(m.requests, m.duration)
	return m
}

// Observe 记一次请求：计数（含状态码档位，错误率由 5xx 档直接算）与
// 耗时分布。
func (m *Metrics) Observe(method, pattern, statusClass string, seconds float64) {
	m.requests.WithLabelValues(method, pattern, statusClass).Inc()
	m.duration.WithLabelValues(method, pattern).Observe(seconds)
}
