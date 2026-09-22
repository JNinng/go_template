package middleware

import (
	"net/http"
)

// corsMiddleware 处理跨域（含 preflight 短路）：位于可观测层外，
// preflight 不进访问日志与指标。实现用 rs/cors——preflight 与
// credentials 的交互、Vary: Origin 的缓存正确性都是它踩过的坑，模板
// 不自养。实例经 Deps.CORS 读取（根包构造期安装、ApplyConfig 热更
// 原子换新）。
func corsMiddleware(d Deps, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := d.CORS()
		if c == nil { // 不可达：根包 New 必装；防御性直通
			next.ServeHTTP(w, r)
			return
		}
		c.Handler(next).ServeHTTP(w, r)
	})
}
