// Package otelc 是内置组件库的 OpenTelemetry 链路追踪组件：装配全局
// TracerProvider（OTLP gRPC/HTTP 导出）并装饰 observ 日志面（ctx 携带
// 有效 span 时 trace_id/span_id 自动附加）。包名拼 c 与
// go.opentelemetry.io/otel 消解同名（约定见库 README）。
//
// 承重行为：无论 endpoint 是否为空都创建并安装 TracerProvider——
// 不导出时应用仍具备 trace_id 生成能力，日志关联照常工作；endpoint
// 非空才创建导出器，把 span 发往 OTLP collector。
//
// 文件布局：otelc.go 组件壳（生命周期）；config.go 配置节；
// provider.go 资源与导出器；logtrace.go 日志装饰（observ 边界）。
//
// 第三方依赖：go.opentelemetry.io/otel + sdk + trace +
// exporters/otlp/otlptrace（grpc/http）+ github.com/jninng/observ
// （删除本目录并 go mod tidy 后即从 go.mod 清除）。
package otelc

import (
	"context"
	"log/slog"
	"sync"

	"github.com/jninng/observ"
	"go.opentelemetry.io/otel"
)

// Tracer 是链路追踪的组件壳：New 装配全局 provider 并装饰日志面，
// Start 输出可见信号，Stop 在停机预算内 flush 未发送的 span。
type Tracer struct {
	cfg   Config
	once  sync.Once
	flush func(context.Context) error // provider Shutdown，幂等由 once 保证
}

// Option 构造选项。
type Option func(*options)

type options struct {
	service string // resource 属性 service.name
	env     string // resource 属性 deployment.environment.name
}

// WithService 设置 span 的资源标识（service.name 与
// deployment.environment.name，对齐 OTel semantic conventions）。
// 模板装配点从应用元数据传入：WithService(meta.Name, effEnv)。
// 缺省不设置——span 仍可用，但聚合侧无法区分服务归属。
func WithService(name, env string) Option {
	return func(o *options) { o.service, o.env = name, env }
}

// New 构造即装配：创建 TracerProvider（endpoint 非空时挂 OTLP 导出器）
// 并安装为全局，随后装饰 observ 日志面——此后动态读 DefaultLogger()
// 的日志输出在 ctx 携带有效 span 时自动附加 trace_id/span_id。构造失败
// 即未启动，无资源需要清理。
func New(cfg Config, opts ...Option) (*Tracer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	tp, err := newTracerProvider(cfg, o)
	if err != nil {
		return nil, err
	}
	otel.SetTracerProvider(tp)
	installLogTrace()
	return &Tracer{cfg: cfg, flush: tp.Shutdown}, nil
}

// Start 输出启动信号后立即返回（provider 在构造期已生效，无后台
// goroutine）；导出属批量异步，由 SDK 自行驱动。
func (t *Tracer) Start(ctx context.Context) error {
	attrs := []slog.Attr{slog.String("protocol", t.cfg.Protocol)}
	if t.cfg.Endpoint == "" {
		attrs = append(attrs, slog.String("export", "off"))
	} else {
		attrs = append(attrs, slog.String("endpoint", t.cfg.Endpoint))
	}
	observ.DefaultLogger().Log(ctx, slog.LevelInfo, "otelc_tracing_started", attrs...)
	return nil
}

// Stop 在停机预算内 flush 未发送的 span；幂等可重入，flush 失败降级为
// 警告日志（不视作停机失败，对齐 zapc 的收尾语义）。
func (t *Tracer) Stop(ctx context.Context) error {
	t.once.Do(func() {
		if err := t.flush(ctx); err != nil {
			observ.DefaultLogger().Log(ctx, slog.LevelWarn, "otelc_tracer_flush_failed",
				slog.Any("error", err))
		}
	})
	return nil
}
