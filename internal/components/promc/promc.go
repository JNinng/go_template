// Package promc 是内置组件库的指标与健康检查组件：Prometheus 私有
// registry（预挂 Go 运行时与进程 collectors）以 otel prometheus 导出器
// 实现 metric.Reader——埋点面（otel metric API，经 SetMeterProvider 安装
// 的全局 MeterProvider）与导出面（client_golang 私有 registry 的 /metrics）
// 由此闭环；暴露形态缺省为 handler 注入（MetricsHandler / HealthHandler
// 挂到业务路由，httpserver 单端口收编），配置独立 addr 时才自起 HTTP
// Server。包名取 prometheus 之意拼 c（对齐 zapc 拼法，约定见库 README）。
//
// New 即安装全局 MeterProvider（otel.SetMeterProvider，与 zapc 接管默认
// 日志同款）：otel 仪表绑定创建时刻的 provider——本组件须先于业务组件
// 接线；先建的仪表固定在全局缺省（noop，零导出），不追溯迁移。未装配
// 本组件时 otel 全局即 noop provider，"缺省不埋点"零依赖达成。
// 基础设施组件需要 label 维度（切片标签）时经 Registry() 直挂
// client_golang collector，与 otel 仪表同场导出。
//
// otel 仪表命名口径：仪表名不带 _total/_seconds 后缀（由导出器按类型与
// 单位追加），counter 配 WithUnit("1")、耗时配 WithUnit("s")——导出的
// 指标名保持 snake_case + 后缀的既定口径（*_total / *_seconds），仪表盘
// 与跨库聚合不受影响。
//
// 健康检查为命名项聚合：全部通过 200，任一失败 503；零登记恒 healthy。
// 跨组件检查由装配点胶水登记（RegisterCheck），组件间互不 import。
//
// 第三方依赖：github.com/prometheus/client_golang +
// go.opentelemetry.io/otel/exporters/prometheus + go.opentelemetry.io/otel
// （删除本目录并 go mod tidy 后即从 go.mod 清除）。
package promc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"

	"go.opentelemetry.io/otel"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/metric"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Prom 是指标与健康检查的组件壳：New 构造 registry 与路由（无副作用、
// 不监听），Start 监听并起服务 goroutine，Stop 优雅关停并回收 provider。
type Prom struct {
	cfg      Config
	srv      *http.Server
	registry *prometheus.Registry
	provider *metric.MeterProvider
	health   *Handler
	metricsH http.Handler
	addr     string // 实际监听地址（Start 时确定；配置为 :0 时端口由内核分配）
	once     sync.Once
}

// New 构造即校验：私有 registry 预挂 Go 运行时与进程 collectors，otel
// prometheus 导出器注册进同一 registry（/metrics 与 /health 同 mux），
// 并把 provider 安装为 otel 全局（仪表绑定构造时刻——其后构造的业务
// 仪表自动路由到本组件出口；多实例构造后者胜，Stop 不回退）。失败即
// 未启动、未安装全局，无资源需要清理。
func New(cfg Config) (*Prom, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewGoCollector(),
	)
	health := NewHandler()
	exp, err := promexporter.New(
		promexporter.WithRegisterer(reg),
		// 资源与 scope 未接线：target_info / scope_info 只会输出空标签
		// 噪声，显式关闭（接线 OTel resource 后可移除）。
		promexporter.WithoutTargetInfo(),
		promexporter.WithoutScopeInfo(),
	)
	if err != nil {
		return nil, fmt.Errorf("promc: prometheus exporter: %w", err)
	}
	provider := metric.NewMeterProvider(metric.WithReader(exp))
	otel.SetMeterProvider(provider)
	metricsH := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	mux := http.NewServeMux()
	mux.Handle(cfg.MetricsPath, metricsH)
	mux.Handle(cfg.HealthPath, health)
	return &Prom{
		cfg:      cfg,
		srv:      &http.Server{Addr: cfg.Addr, Handler: mux},
		registry: reg,
		provider: provider,
		health:   health,
		metricsH: metricsH,
	}, nil
}

// Meter 返回全局 provider 下指定 instrumentation scope 的 otel 仪表面。
// scope 惯例传调用方 import 路径（对齐 OTel 语义约定）；仪表的 New* 属
// 构造期调用（禁止热路径），且须晚于本组件接线（否则绑定 noop 全局、
// 永不导出）。未注入场景也可直接用 otel.GetMeterProvider().Meter(scope)
// ——全局即本组件安装的 provider。在私有 registry 上注册的自定义
// collector 不会出现在 /metrics——一律经 Registry() 注册。
func (p *Prom) Meter(scope string) otelmetric.Meter { return p.provider.Meter(scope) }

// Registry 返回私有 registry（注册自定义 prometheus.Collector 用）。
func (p *Prom) Registry() prometheus.Registerer { return p.registry }

// RegisterCheck 登记一项命名健康检查；跨组件检查由装配点胶水登记
// （组件间零依赖，菜单模型不破）。
func (p *Prom) RegisterCheck(name string, fn CheckFunc) { p.health.Register(name, fn) }

// MetricsHandler 返回 /metrics 等价 handler（单端口注入用：挂到业务
// 路由，见 README 配方）。HealthHandler 同。
func (p *Prom) MetricsHandler() http.Handler { return p.metricsH }

// HealthHandler 返回健康检查等价 handler（单端口注入用）。
func (p *Prom) HealthHandler() http.Handler { return p.health }

// MetricsPath 返回配置的指标暴露路径（单端口挂载方对齐用）。
func (p *Prom) MetricsPath() string { return p.cfg.MetricsPath }

// HealthPath 返回配置的健康检查路径（单端口挂载方对齐用）。
func (p *Prom) HealthPath() string { return p.cfg.HealthPath }

// Start 同步监听（端口占用等绑定错误在此暴露，fail-fast）后起服务
// goroutine，立即返回。addr 为空时跳过监听（单端口形态：端点经
// MetricsHandler / HealthHandler 挂在业务路由上，本组件无独立端口）。
// Section 返回本组件的配置节名（统一节名获取接口，恒返回 SectionName）。
func (p *Prom) Section() string { return SectionName }

func (p *Prom) Start(ctx context.Context) error {
	if p.cfg.Addr == "" {
		slog.InfoContext(ctx, "promc_server_skipped",
			slog.String("reason", "addr_empty"),
			slog.String("metrics_path", p.cfg.MetricsPath),
			slog.String("health_path", p.cfg.HealthPath))
		return nil
	}
	ln, err := net.Listen("tcp", p.cfg.Addr)
	if err != nil {
		return fmt.Errorf("promc: listen %q: %w", p.cfg.Addr, err)
	}
	p.addr = ln.Addr().String()
	go func() {
		if err := p.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// 动态读 slog.Default()：server goroutine 生命周期横跨日志装配
			slog.WarnContext(context.Background(), "promc_server_error",
				slog.Any("error", err))
		}
	}()
	slog.InfoContext(ctx, "promc_server_started",
		slog.String("addr", p.addr),
		slog.String("metrics_path", p.cfg.MetricsPath),
		slog.String("health_path", p.cfg.HealthPath))
	return nil
}

// Addr 返回实际监听地址（Start 前为配置值；配置 :0 时端口由内核分配，
// Start 后可取到实际值）。
func (p *Prom) Addr() string {
	if p.addr != "" {
		return p.addr
	}
	return p.cfg.Addr
}

// Stop 在停机预算内优雅关停（排空在途请求）并回收 provider（刷尽
// 导出器在途状态）；幂等可重入，失败降级为警告日志（不视作停机失败）。
// 全局 provider 指派不回退（进程收尾语义）。addr 为空（未自起 server）
// 时仍回收 provider。
func (p *Prom) Stop(ctx context.Context) error {
	p.once.Do(func() {
		if p.cfg.Addr != "" {
			if err := p.srv.Shutdown(ctx); err != nil {
				slog.WarnContext(ctx, "promc_server_stop_failed",
					slog.Any("error", err))
			}
		}
		if err := p.provider.Shutdown(ctx); err != nil {
			slog.WarnContext(ctx, "promc_provider_shutdown_failed",
				slog.Any("error", err))
		}
	})
	return nil
}
