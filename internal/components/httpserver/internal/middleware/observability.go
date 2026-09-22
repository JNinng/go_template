package middleware

import (
	"fmt"
	"net/http"
	"time"

	"go.uber.org/zap"

	"go_template/internal/components/httpserver/internal/metric"
	"go_template/internal/components/httpserver/internal/trust"
	"go_template/pkg/safe"
)

// instanceHeader 是实例 ID 的响应头名；值为逗号分隔的实例 ID 清单。
const instanceHeader = "X-Instance-IDs"

// observability 承载访问日志与指标上报，并为所有响应注入 X-Instance-IDs。
// 位于 tracing 外层：请求返回后从共享状态取 request_id / trace_id 记
// 日志、上报指标。跳过清单（Skip，探活/就绪/版本/指标/健康/pprof）
// 不记日志、不计指标，但实例头照常注入、tracing span 照常生成（链路
// 排障需要）。
func observability(d Deps, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		w.Header().Set(instanceHeader, d.InstanceIDs)
		if d.Skip(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		st := stateFromContext(r.Context())
		var bc *countingReader
		if r.Body != nil {
			bc = &countingReader{rc: r.Body}
			r.Body = bc
		}
		rec := &respRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		// 路由模板由 RequestID 层（tracing 内层）在 mux 匹配后回填进
		// 共享状态并就地完成 span 改名（span 在 otelhttp 返回时已 End，
		// 外层改名会被丢弃）。
		pattern := stPattern(st)
		if d.Metrics != nil {
			d.Metrics.Observe(r.Method, patternLabel(pattern), statusClass(rec.status),
				time.Since(start).Seconds())
		}

		code := rec.status
		if code == 0 {
			code = http.StatusOK
		}
		fields := []zap.Field{
			zap.String("method", r.Method),
			zap.String("path", safe.Truncate(r.URL.Path, 512)),
			zap.Int("status_code", code),
			zap.Float64("duration_ms", float64(time.Since(start).Nanoseconds())/1e6),
			zap.String("client_ip", trust.ClientIP(r, d.Proxies())),
			zap.String("request_id", stRequestID(st)),
			zap.String("user_agent", safe.Truncate(r.UserAgent(), 256)),
		}
		if pattern != "" {
			fields = append(fields, zap.String("route", pattern))
		}
		if st != nil && st.traceID != "" {
			fields = append(fields, zap.String("trace_id", st.traceID))
		}
		if ref := r.Referer(); ref != "" {
			fields = append(fields, zap.String("referer", safe.Truncate(ref, 256)))
		}
		var bodyBytes int64
		if bc != nil {
			bodyBytes = bc.count
		}
		fields = append(fields,
			zap.Int64("request_bytes", bodyBytes),
			zap.Int64("response_bytes", rec.bytes))

		switch {
		case code >= 500:
			zap.L().Error("httpserver_request_completed", fields...)
		case code >= 400:
			zap.L().Warn("httpserver_request_completed", fields...)
		default:
			zap.L().Info("httpserver_request_completed", fields...)
		}
	})
}

// patternLabel 把空模板（未匹配路由）折叠为固定标签值，防基数泄漏。
func patternLabel(pattern string) string {
	if pattern == "" {
		return metric.UnmatchedPattern
	}
	return pattern
}

// statusClass 把状态码折叠为档位（"2xx".."5xx"），错误率由 5xx 档直接算。
func statusClass(code int) string {
	return fmt.Sprintf("%dxx", code/100)
}

func stRequestID(st *reqState) string {
	if st == nil {
		return ""
	}
	return st.requestID
}

func stPattern(st *reqState) string {
	if st == nil {
		return ""
	}
	return st.pattern
}
