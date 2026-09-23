// Package promc 是内置组件库的指标与健康检查组件：Prometheus 私有
// registry（预挂 Go 运行时与进程 collectors）以 adapters/prom 实现
// observ.Meter——埋点面（observ.Meter）与导出面（client_golang 私有
// registry）由此闭环；暴露形态缺省为 handler 注入（MetricsHandler /
// HealthHandler 挂到业务路由，httpserver 单端口收编），配置独立
// addr 时才自起 HTTP Server。包名取 prometheus 之意拼 c（对齐 zapc
// 拼法，约定见库 README）。
//
// New 即安装 observ 默认 Meter（DefaultMeter，与 zapc 接管默认日志
// 同款）：其后构造的业务组件未注入 Meter 时构造期回落本组件的出口，
// 免逐组件穿线。仪器绑定构造时刻——本组件须先于业务组件接线；显式
// WithMeter 注入仍可覆盖默认。
//
// 健康检查为命名项聚合：全部通过 200，任一失败 503；零登记恒 healthy。
// 跨组件检查由装配点胶水登记（RegisterCheck），组件间互不 import。
//
// 第三方依赖：github.com/prometheus/client_golang +
// github.com/jninng/observ/adapters/prom + github.com/jninng/observ
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

	"github.com/jninng/observ"
	promAdapter "github.com/jninng/observ/adapters/prom"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Prom 是指标与健康检查的组件壳：New 构造 registry 与路由（无副作用、
// 不监听），Start 监听并起服务 goroutine，Stop 优雅关停。
type Prom struct {
	cfg      Config
	srv      *http.Server
	registry *prometheus.Registry
	meter    observ.Meter
	health   *Handler
	metricsH http.Handler
	addr     string // 实际监听地址（Start 时确定；配置为 :0 时端口由内核分配）
	once     sync.Once
}

// New 构造即校验：私有 registry 预挂 Go 运行时与进程 collectors，
// /metrics 与 /health 同 mux；并把 meter 安装为 observ 默认（构造期
// 快照语义——其后构造的组件回落本出口，已建仪表不迁移、不追溯，故
// 本组件须先于业务组件接线；多实例构造后者胜，Stop 不回退）。失败即
// 未启动、未安装默认，无资源需要清理。
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
	metricsH := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	mux := http.NewServeMux()
	mux.Handle(cfg.MetricsPath, metricsH)
	mux.Handle(cfg.HealthPath, health)
	meter := promAdapter.New(reg)
	observ.SetDefaultMeter(meter)
	return &Prom{
		cfg:      cfg,
		srv:      &http.Server{Addr: cfg.Addr, Handler: mux},
		registry: reg,
		meter:    meter,
		health:   health,
		metricsH: metricsH,
	}, nil
}

// Meter 返回注册到私有 registry 的 observ.Meter。New* 属构造期调用
// （禁止热路径，见 observ 契约）；装配点把它经 WithMeter 注入业务组件。
// 在 prometheus 默认 registry 上注册的自定义 collector 不会出现在
// /metrics——一律经本 Meter 或 Registry() 注册。
func (p *Prom) Meter() observ.Meter { return p.meter }

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
		observ.DefaultLogger().Log(ctx, slog.LevelInfo, "promc_server_skipped",
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
			observ.DefaultLogger().Log(context.Background(), slog.LevelWarn, "promc_server_error",
				slog.Any("error", err))
		}
	}()
	observ.DefaultLogger().Log(ctx, slog.LevelInfo, "promc_server_started",
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

// Stop 在停机预算内优雅关停（排空在途请求）；幂等可重入，关停失败
// 降级为警告日志（不视作停机失败）。addr 为空（未自起 server）时空操作。
func (p *Prom) Stop(ctx context.Context) error {
	p.once.Do(func() {
		if p.cfg.Addr == "" {
			return
		}
		if err := p.srv.Shutdown(ctx); err != nil {
			observ.DefaultLogger().Log(ctx, slog.LevelWarn, "promc_server_stop_failed",
				slog.Any("error", err))
		}
	})
	return nil
}
