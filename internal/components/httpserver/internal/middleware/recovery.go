package middleware

import (
	"context"
	"net/http"

	"go.uber.org/zap"

	"go_template/internal/components/httpserver/internal/trust"
	"go_template/pkg/safe"
)

// recovery 是最外层中间件：捕获链上任何一层的 panic（含业务 handler），
// 记专门日志防止服务崩溃。panic 请求不经过访问日志与指标（panic 展栈
// 跳过了可观测层的收尾）——信息由本层的 httpserver_panic_recovered
// 承载，字段口径与访问日志对齐。
//
// 响应未开头时写 500 JSON；已开头（无法改写状态码）只记日志。
func recovery(d Deps, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := &reqState{}
		rec := &respRecorder{ResponseWriter: w}
		r = r.WithContext(context.WithValue(r.Context(), stateCtxKey{}, st))
		defer func() {
			p := recover()
			if p == nil {
				return
			}
			zap.L().Error("httpserver_panic_recovered",
				zap.String("method", r.Method),
				zap.String("path", safe.Truncate(r.URL.Path, 512)),
				zap.Int("status_code", http.StatusInternalServerError),
				zap.String("client_ip", trust.ClientIP(r, d.Proxies())),
				zap.String("request_id", st.requestID),
				zap.String("trace_id", st.traceID),
				zap.Any("panic_value", p),
				zap.Stack("stack"))
			if !rec.wrote {
				rec.Header().Set("Content-Type", "application/json")
				rec.WriteHeader(http.StatusInternalServerError)
				_, _ = rec.Write([]byte(`{"error":"internal server error"}` + "\n"))
			}
		}()
		next.ServeHTTP(rec, r)
	})
}
