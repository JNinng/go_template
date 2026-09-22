package middleware

import (
	"encoding/json"
	"net/http"
)

// bodyLimit 施加请求体上限（Deps.BodyLimit，热更生效）：
//
//   - ContentLength 已知且超限 → 直接 413（不读体，确定性拒绝）；
//   - 其余（chunked / 谎报长度）→ http.MaxBytesReader 包装，业务读体
//     超限时读到错误；业务未写响应即返回时 net/http 自动补 413。
//
// 位于链最内层、包住整个路由表：业务路由与内置端点同受约束——探活等
// 内置端点不读请求体，该上限实际约束的是业务上传。
// limit <= 0 表示不限制（直通）。
func bodyLimit(d Deps, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := d.BodyLimit()
		if limit <= 0 {
			next.ServeHTTP(w, r)
			return
		}
		if r.ContentLength > limit {
			writeJSON(w, http.StatusRequestEntityTooLarge,
				map[string]string{"error": "request body too large"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

// writeJSON 写 JSON 错误响应（请求体超限 413 用）。
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
