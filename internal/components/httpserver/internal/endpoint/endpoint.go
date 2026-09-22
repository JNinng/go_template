// Package endpoint 提供 httpserver 的内置端点（探活/就绪/版本/pprof）。
// 组件 internal 子包：挂载与路径冲突检查由根包 New 统一驱动。
package endpoint

import (
	"encoding/json"
	"net/http"
	"net/http/pprof"

	"go_template/pkg/version"
)

// Route 是一条待挂载的路由（pattern 语义同 http.ServeMux）。
type Route struct {
	Pattern string
	Handler http.Handler
}

// writeJSON 写 JSON 响应。
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// methodGET 仅放行 GET，其余 405（RFC 9110：携带 Allow）。
func methodGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

// Liveness 是存活探针：进程活着即 200——排空期（正在停机）也保持 200，
// 摘流信号由 readiness 承担（k8s 语义：liveness 翻红会重启 pod，
// 停机窗口触发重启是错误信号）。
func Liveness() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !methodGET(w, r) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
}

// Readiness 是就绪探针：排空中（draining 返回 true，停机摘流信号）或
// health 聚合检查任一失败 → 503 摘除流量；否则 200。health 传 nil 时
// 仅反映摘流信号。
func Readiness(draining func() bool, health http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !methodGET(w, r) {
			return
		}
		if draining() {
			writeJSON(w, http.StatusServiceUnavailable,
				map[string]string{"status": "unavailable", "reason": "draining"})
			return
		}
		if health != nil {
			health.ServeHTTP(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
}

// Version 是版本端点：返回构建期注入的五字段版本元数据（pkg/version）。
func Version() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !methodGET(w, r) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"version":    version.Version,
			"commit":     version.Commit,
			"date":       version.Date,
			"build_time": version.BuildTime,
			"go_version": version.GoVersion,
		})
	})
}

// Pprof 返回 pprof 端点组（net/http/pprof 标准 handler）。
func Pprof() []Route {
	return []Route{
		{Pattern: "/debug/pprof/", Handler: http.HandlerFunc(pprof.Index)},
		{Pattern: "/debug/pprof/cmdline", Handler: http.HandlerFunc(pprof.Cmdline)},
		{Pattern: "/debug/pprof/profile", Handler: http.HandlerFunc(pprof.Profile)},
		{Pattern: "/debug/pprof/symbol", Handler: http.HandlerFunc(pprof.Symbol)},
		{Pattern: "/debug/pprof/trace", Handler: http.HandlerFunc(pprof.Trace)},
	}
}
