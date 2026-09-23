// Package httpserver 是内置组件库的业务 HTTP Server 组件：标准库
// ServeMux 路由 + 可观测中间件链（Recovery → CORS → 访问日志/指标 →
// otelhttp tracing → RequestID → 请求体上限），强绑定模板可观测设施
// ——请求日志经 WithAccessLogger 注入独立记录器（未注入回落 zap
// 全局），tracing 走 otelc 安装的 otel 全局，指标注册到
// promc 的私有 registry（经 WithProm 注入的 PromProvider），排空摘流
// 复用 promc 健康检查聚合。
//
// 承重行为：
//
//   - 单端口收编：promc 的 /metrics /health 经 PromProvider 挂进业务
//     路由（promc.addr 留空即不自起独立 server），一个端口承载全部；
//   - 响应统一携带 X-Instance-IDs（实例 ID = 服务名:主机名，见
//     InstanceID）、X-Request-ID（提取合法请求头，否则 TraceID 兜底）
//     与 X-Trace-ID（本服务端 span 的 TraceID，span 有效时）；
//   - 停机三步走（drain_aware）：readiness 翻 503 摘流 → 关 keep-alive
//     （响应带 Connection: close）→ Shutdown 排空在途请求。
//
// 文件布局：httpserver.go 组件壳（生命周期、路由与挂载）；config.go
// 配置节；handler.go 中间件链组装。实现细节在 internal/ 子包，公共
// 契约全部由本包出口：middleware（链与六个中间件）、trust（可信代理
// 网段与客户端 IP）、metric（预置指标）、endpoint（内置端点）、
// instance（实例 ID）——internal 机制保证子包只被本组件引用。
//
// 第三方依赖：go.opentelemetry.io/contrib（otelhttp）+
// go.opentelemetry.io/otel（trace/semconv/attribute）+ github.com/rs/cors
// + github.com/prometheus/client_golang + go.uber.org/zap（zapc 已携带）
// + github.com/jninng/observ + gopkg.in/yaml.v3（删除本目录并
// go mod tidy 后即从 go.mod 清除）。
package httpserver

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jninng/observ"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/cors"
	"go.uber.org/zap"

	"go_template/internal/components/httpserver/internal/ctxlog"
	"go_template/internal/components/httpserver/internal/endpoint"
	"go_template/internal/components/httpserver/internal/instance"
	"go_template/internal/components/httpserver/internal/metric"
	"go_template/internal/components/httpserver/internal/trust"
	"go_template/pkg/ctxkey"
)

// PromProvider 是与 promc 的接缝：装配点传 *promc.Prom 即满足（结构化
// 类型）——本包不 import promc，组件间零依赖约定不破（ADR-0001），
// "强绑定"由装配点接线表达。注入后：metrics/health 挂进业务路由
// （路径以 promc 配置为单一事实源）、指标注册其私有 registry、
// readiness 聚合其健康检查。
type PromProvider interface {
	MetricsHandler() http.Handler
	HealthHandler() http.Handler
	MetricsPath() string
	HealthPath() string
	Registry() prometheus.Registerer
}

// Option 构造选项。
type Option func(*options)

type options struct {
	service   string // 服务名（实例 ID 前缀；启动日志）
	env       string // 环境声明（启动日志）
	version   string // 版本（启动日志）
	prom      PromProvider
	accessLog func() *zap.Logger // 请求日志记录器（WithAccessLogger 注入；nil 回落 zap 全局）
}

// WithService 设置服务标识：服务名进实例 ID（{service}:{hostname}）与
// 启动日志；模板装配点从应用元数据与构建元数据传入：
// WithService(meta.Name, meta.Env, version.Version)。缺省实例 ID 退化为
// 裸 hostname。
func WithService(name, env, ver string) Option {
	return func(o *options) { o.service, o.env, o.version = name, env, ver }
}

// WithProm 注入 promc 组件（单端口收编的接线点）。未注入时：metrics/
// health 路径不存在、指标注册到包内私有 registry（无处导出）、
// readiness 仅反映摘流信号。
func WithProm(p PromProvider) Option {
	return func(o *options) { o.prom = p }
}

// WithAccessLogger 注入请求日志记录器：访问日志与 panic 日志走它，
// 与业务日志分文件（装配点从 zapc 节派生独立实例，如固定写 req.log）。
// getter 每请求调用、须返回当前生效实例（zapc.LoggerKit.Current 方法
// 值即热更安全的取用）；nil 或返回 nil 回落 zap 全局。ctx 日志
// （LoggerFrom）不经此通道——业务日志恒走 zap 全局（应用流），req.log
// 只放访问记录。
func WithAccessLogger(get func() *zap.Logger) Option {
	return func(o *options) { o.accessLog = get }
}

// LoggerFrom 取请求作用域日志记录器：zap 全局逐请求派生、预绑定
// request_id / trace_id / span_id（RequestID 层挂进 ctx），业务 handler
// 深处免逐层穿字段。非请求语境回落未富化的 zap 全局（缺链路字段即
// "不在请求链路内"的信号）。注意：直调 zap.L() 与本访问器同源但无
// 预绑定字段——请求内日志一律走本访问器。
func LoggerFrom(ctx context.Context) *zap.Logger {
	return ctxlog.From(ctx)
}

// Server 是业务 HTTP Server 的组件壳：New 构造并挂载内置端点（不监听），
// Start 组装中间件链并同步监听（fail-fast），Stop 排空停机（幂等）。
// 业务路由经 Handle / HandleFunc 在 Start 前注册（路由表监听前冻结）。
type Server struct {
	cfg Config // 构造期冻结的配置事实源（冷字段；热字段走下方原子态）

	mux     *http.ServeMux
	handler http.Handler // Start 时组装的完整链
	srv     *http.Server
	addr    string // 实际监听地址（Start 时确定；:0 由内核分配）

	opt         options
	prom        PromProvider
	metrics     *metric.Metrics
	metricsPath string // promc 挂载路径快照（跳过清单联动）
	healthPath  string
	instanceIDs string // X-Instance-IDs 值（构造期冻结）

	proxies      atomic.Pointer[trust.Table]
	bodyLimit    atomic.Int64
	drainAware   atomic.Bool
	corsInstance atomic.Pointer[cors.Cors]
	drain        atomic.Bool // 排空中（readiness 摘流信号）
	once         sync.Once   // Stop 幂等
	started      atomic.Bool // 路由注册窗口闸门（Start 后 Handle panic）
}

// New 构造即校验：配置拒绝标准见 Config.Validate；TLS 启用时证书文件
// 在此加载（fail-fast，路径/口令错误不拖到监听期）；随后挂载内置端点
// （探活/就绪/版本/pprof/promc）。失败即未启动，无资源需要清理。
func New(cfg Config, opts ...Option) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if cfg.CertFile != "" {
		if _, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile); err != nil {
			return nil, fmt.Errorf("httpserver: load TLS keypair: %w", err)
		}
	}
	proxies, err := trust.NewTable(cfg.TrustedProxies)
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg: cfg,
		mux: http.NewServeMux(),
		opt: o,
	}
	if proxies != nil {
		s.proxies.Store(&proxies)
	}
	s.bodyLimit.Store(cfg.MaxBodySize)
	s.drainAware.Store(cfg.DrainAware)
	s.corsInstance.Store(buildCors(cfg.CORS))
	ids := append([]string{instance.ID(o.service)}, cfg.ExtraInstanceIDs...)
	s.instanceIDs = strings.Join(ids, ",")
	if err := s.mount(); err != nil {
		return nil, err
	}
	// metrics 最后注册：mount 失败时未向外部 registry 注册任何 collector
	//（DESIGN §11.2：半构造资源由 New 内部回收）。未注入 promc 时落到
	// 包内私有 registry——指标照常计数，仅无处导出。
	var reg prometheus.Registerer = prometheus.NewRegistry()
	if o.prom != nil {
		reg = o.prom.Registry()
	}
	s.metrics = metric.New(reg)
	return s, nil
}

// mount 挂载内置端点；模式冲突（如 liveness 与 promc 路径撞车）在
// 构造期拒绝。
func (s *Server) mount() error {
	routes := []endpoint.Route{
		{Pattern: s.cfg.Liveness, Handler: endpoint.Liveness()},
		{Pattern: s.cfg.Readiness, Handler: endpoint.Readiness(
			s.drain.Load, s.promHealth())},
		{Pattern: s.cfg.Versions, Handler: endpoint.Version()},
	}
	if s.opt.prom != nil {
		s.prom = s.opt.prom
		s.metricsPath = s.prom.MetricsPath()
		s.healthPath = s.prom.HealthPath()
		routes = append(routes,
			endpoint.Route{Pattern: s.metricsPath, Handler: s.prom.MetricsHandler()},
			endpoint.Route{Pattern: s.healthPath, Handler: s.prom.HealthHandler()})
	}
	if s.cfg.Pprof {
		routes = append(routes, endpoint.Pprof()...)
	}
	seen := make(map[string]bool, len(routes))
	for _, rt := range routes {
		if seen[rt.Pattern] {
			return fmt.Errorf("httpserver: duplicate route pattern %q (内置端点路径冲突，检查 liveness/readiness/versions 与 promc 路径)", rt.Pattern)
		}
		seen[rt.Pattern] = true
		s.mux.Handle(rt.Pattern, rt.Handler)
	}
	return nil
}

// promHealth 返回 readiness 委托的健康聚合 handler（未注入 promc 为
// nil——仅反映摘流信号）。
func (s *Server) promHealth() http.Handler {
	if s.opt.prom != nil {
		return s.opt.prom.HealthHandler()
	}
	return nil
}

// Handle 注册业务路由（透传内部 ServeMux；支持方法与通配符模式，如
// "POST /api/{id}"）。仅 Start 前允许；Start 后调用 panic（路由表在
// 监听前冻结，运行时动态注册不在模板范围）。模式非法或与既有路由
// 冲突时 ServeMux 当场 panic——装配期暴露。
func (s *Server) Handle(pattern string, h http.Handler) {
	if s.started.Load() {
		panic("httpserver: Handle after Start is not allowed（路由表在监听前冻结，业务路由须在装配期注册）")
	}
	s.mux.Handle(pattern, h)
}

// HandleFunc 是 Handle 的函数形态。
func (s *Server) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	if s.started.Load() {
		panic("httpserver: HandleFunc after Start is not allowed（路由表在监听前冻结，业务路由须在装配期注册）")
	}
	s.mux.HandleFunc(pattern, h)
}

// Start 组装中间件链并同步监听（端口占用等绑定错误在此暴露，
// fail-fast）后起服务 goroutine。TLS 由 cert_file/key_file 非空启用
// （单监听 HTTPS；HTTP+HTTPS 双监听不在范围）。
// Section 返回本组件的配置节名（统一节名获取接口，恒返回 SectionName）。
func (s *Server) Section() string { return SectionName }

func (s *Server) Start(ctx context.Context) error {
	s.started.Store(true) // 关闭注册窗口（此后 Handle panic）
	s.handler = s.buildHandler()
	srv := &http.Server{
		Addr:           s.cfg.Addr,
		Handler:        s.handler,
		ReadTimeout:    time.Duration(s.cfg.ReadTimeout),
		WriteTimeout:   time.Duration(s.cfg.WriteTimeout),
		IdleTimeout:    time.Duration(s.cfg.IdleTimeout),
		MaxHeaderBytes: s.cfg.MaxHeaderBytes,
	}
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("httpserver: listen %q: %w", s.cfg.Addr, err)
	}
	s.srv = srv
	s.addr = ln.Addr().String()
	go func() {
		var err error
		if s.cfg.CertFile != "" {
			err = srv.ServeTLS(ln, s.cfg.CertFile, s.cfg.KeyFile)
		} else {
			err = srv.Serve(ln)
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			// 技术故障（IO/连接）：Error + stack，仅在错误最底层打一次（§9 日志规范）
			observ.DefaultLogger().Log(context.Background(), slog.LevelError, "httpserver_server_error",
				slog.Any("error", err),
				slog.String("stack", string(debug.Stack())))
		}
	}()
	attrs := []slog.Attr{
		slog.String("addr", s.addr),
		slog.String("network", s.network()),
		slog.String("instance_ids", s.instanceIDs),
	}
	if s.opt.service != "" {
		attrs = append(attrs, slog.String("service_name", s.opt.service))
	}
	if s.opt.env != "" {
		attrs = append(attrs, slog.String("app_env", s.opt.env))
	}
	if s.opt.version != "" {
		attrs = append(attrs, slog.String("app_version", s.opt.version))
	}
	observ.DefaultLogger().Log(ctx, slog.LevelInfo, "httpserver_server_started", attrs...)
	return nil
}

func (s *Server) network() string {
	if s.cfg.CertFile != "" {
		return "https"
	}
	return "http"
}

// Addr 返回实际监听地址（Start 前为配置值；配置 :0 时端口由内核分配，
// Start 后可取到实际值——nacos 注册等装配点经此取端口）。
func (s *Server) Addr() string {
	if s.addr != "" {
		return s.addr
	}
	return s.cfg.Addr
}

// Stop 优雅停机三步走（drain_aware，缺省开）：readiness 翻 503 摘流 →
// 关 keep-alive（其后响应带 Connection: close）→ Shutdown 在停机预算内
// 排空在途请求；drain_aware 关闭时跳过前两步直接 Shutdown。幂等可重入，
// Shutdown 失败降级为警告日志（不视作停机失败）。
func (s *Server) Stop(ctx context.Context) error {
	s.once.Do(func() {
		if !s.started.Load() || s.srv == nil {
			return // 未 Start：无资源
		}
		observ.DefaultLogger().Log(ctx, slog.LevelInfo, "httpserver_server_stopping",
			slog.Bool("drain_aware", s.drainAware.Load()))
		if s.drainAware.Load() {
			s.drain.Store(true)               // readiness 摘流：负载均衡在下一个探测周期摘除本实例
			s.srv.SetKeepAlivesEnabled(false) // 新响应带 Connection: close，客户端不复用
		}
		begin := time.Now()
		if err := s.srv.Shutdown(ctx); err != nil {
			observ.DefaultLogger().Log(ctx, slog.LevelWarn, "httpserver_server_stop_failed",
				slog.Any("error", err))
		}
		observ.DefaultLogger().Log(ctx, slog.LevelInfo, "httpserver_server_stopped",
			slog.Int64("drain_ms", time.Since(begin).Milliseconds()))
	})
	return nil
}

// ApplyConfig 热更：max_body_size / trusted_proxies / drain_aware / cors.*
// 原子生效；冷字段（addr、超时、max_header_bytes、路径、TLS、pprof、
// extra_instance_ids）变更记警告 httpserver_config_restart_required 后
// 不生效（维持构造期值）——http.Server 运行中改字段属数据竞争，
// 监听器重建（换端口/换证书）不在热更范围。拒绝标准与构造期共用
// （Validate），非法配置返回 error（config.Watch 记日志，不影响旧配置
// 继续生效）。
func (s *Server) ApplyConfig(cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if changed := coldChanges(s.cfg, cfg); len(changed) > 0 {
		observ.DefaultLogger().Log(context.Background(), slog.LevelWarn,
			"httpserver_config_restart_required",
			slog.String("changed_fields", strings.Join(changed, ",")))
	}
	proxies, err := trust.NewTable(cfg.TrustedProxies)
	if err != nil {
		return err
	}
	if proxies != nil {
		s.proxies.Store(&proxies)
	} else {
		s.proxies.Store(&trust.Table{}) // 空清单：显式落空表（不信任任何代理）
	}
	s.bodyLimit.Store(cfg.MaxBodySize)
	s.drainAware.Store(cfg.DrainAware)
	s.corsInstance.Store(buildCors(cfg.CORS))
	return nil
}

// coldChanges 对比冷字段，返回发生变化的字段名（yaml 键名）。
func coldChanges(old, new Config) []string {
	var changed []string
	if old.Addr != new.Addr {
		changed = append(changed, "addr")
	}
	if old.ReadTimeout != new.ReadTimeout {
		changed = append(changed, "read_timeout")
	}
	if old.WriteTimeout != new.WriteTimeout {
		changed = append(changed, "write_timeout")
	}
	if old.IdleTimeout != new.IdleTimeout {
		changed = append(changed, "idle_timeout")
	}
	if old.MaxHeaderBytes != new.MaxHeaderBytes {
		changed = append(changed, "max_header_bytes")
	}
	if old.Liveness != new.Liveness {
		changed = append(changed, "liveness")
	}
	if old.Readiness != new.Readiness {
		changed = append(changed, "readiness")
	}
	if old.Versions != new.Versions {
		changed = append(changed, "versions")
	}
	if old.CertFile != new.CertFile || old.KeyFile != new.KeyFile {
		changed = append(changed, "cert_file/key_file")
	}
	if old.Pprof != new.Pprof {
		changed = append(changed, "pprof")
	}
	if !slices.Equal(old.ExtraInstanceIDs, new.ExtraInstanceIDs) {
		changed = append(changed, "extra_instance_ids")
	}
	return changed
}

// skipObservability 判定路径是否在跳过清单：探活/就绪/版本（本组件
// 配置）+ 指标/健康（WithProm 挂载的路径）+ pprof 子树。均为冷字段，
// 构造期冻结。
func (s *Server) skipObservability(path string) bool {
	if path == s.cfg.Liveness || path == s.cfg.Readiness || path == s.cfg.Versions {
		return true
	}
	if s.prom != nil && (path == s.metricsPath || path == s.healthPath) {
		return true
	}
	return path == "/debug/pprof" || strings.HasPrefix(path, "/debug/pprof/")
}

// InstanceID 解析服务实例 ID（internal/instance 的稳定出口）：
// INSTANCE_ID 环境变量 > {service}:{HOSTNAME} > {service}:unknown。
// 响应头 X-Instance-IDs、otelc 的 service.instance.id 与启动日志共用
// 同一值——本函数是单一事实源。
func InstanceID(service string) string { return instance.ID(service) }

// RequestID 从 ctx 取 RequestID（业务侧助手）：值由中间件注入，业务
// handler 里 httpserver.RequestID(r.Context()) 即得；未携带（不经本
// 组件的调用路径）返回空串。
func RequestID(ctx context.Context) string { return ctxkey.RequestID(ctx) }
