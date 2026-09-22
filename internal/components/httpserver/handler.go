package httpserver

import (
	"net/http"

	"github.com/rs/cors"

	"go_template/internal/components/httpserver/internal/middleware"
	"go_template/internal/components/httpserver/internal/trust"
)

// buildHandler 组装中间件链并接上路由表（Start 时一次，此后热更只换
// 原子态、不换链）。链序与各层落位理由见 internal/middleware 包注释：
//
//	Recovery → CORS → Observability → Tracing → RequestID → MaxBodySize → mux
func (s *Server) buildHandler() http.Handler {
	return middleware.Chain(middleware.Deps{
		Proxies:     s.currentProxies,
		InstanceIDs: s.instanceIDs,
		Metrics:     s.metrics,
		Skip:        s.skipObservability,
		BodyLimit:   func() int64 { return s.bodyLimit.Load() },
		CORS:        s.currentCORS,
	}, s.mux)
}

// currentProxies 读取当前可信网段快照（trusted_proxies 热更的整体替换点）。
func (s *Server) currentProxies() trust.Table {
	if p := s.proxies.Load(); p != nil {
		return *p
	}
	return nil
}

// currentCORS 读取当前跨域实例（cors.* 热更的整体替换点）。
func (s *Server) currentCORS() *cors.Cors {
	return s.corsInstance.Load()
}

// buildCors 由配置构造 rs/cors 实例（New 与 ApplyConfig 共用）。
// MaxAge 直接取秒数（rs/cors 的 MaxAge 即 int 秒；0 = 不下发该头）。
func buildCors(c CORSConfig) *cors.Cors {
	return cors.New(cors.Options{
		AllowedOrigins:   c.AllowedOrigins,
		AllowedMethods:   c.AllowedMethods,
		AllowedHeaders:   c.AllowedHeaders,
		AllowCredentials: c.AllowCredentials,
		MaxAge:           c.MaxAgeSeconds,
	})
}
