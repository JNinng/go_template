// Package otelc 是内置组件库的 OpenTelemetry 可观测组件：装配全局
// TracerProvider（OTLP gRPC/HTTP 导出）并装饰 observ 日志面（ctx 携带
// 有效 span 时 trace_id/span_id 自动附加）；logs_enabled 时另建 OTLP
// 日志导出管线（otelzap 桥为 zap core，经装配点组合进 zapc 的 tee）。
// 包名拼 c 与 go.opentelemetry.io/otel 消解同名（约定见库 README）。
//
// 承重行为：无论 endpoint 是否为空都创建并安装 TracerProvider——
// 不导出时应用仍具备 trace_id 生成能力，日志关联照常工作；endpoint
// 非空才创建导出器，把 span 发往 OTLP collector。
//
// 文件布局：otelc.go 组件壳（生命周期）；config.go 配置节；
// provider.go 资源与 tracing 导出器；logs.go OTLP 日志导出管线；
// logtrace.go 日志装饰（observ 边界）。
//
// 第三方依赖：go.opentelemetry.io/otel + sdk + sdk/log + trace +
// exporters/otlp/otlptrace（grpc/http）+ exporters/otlp/otlplog（grpc/http）
// + contrib/bridges/otelzap（zap 桥）+ github.com/jninng/observ
// （删除本目录并 go mod tidy 后即从 go.mod 清除；otelzap 桥仅在启用
// logs_enabled 时 import zap——zapc 已携带该依赖，不新增）。
package otelc

import (
	"context"
	"log/slog"
	"sync"

	"github.com/jninng/observ"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.uber.org/zap/zapcore"
)

// Tracer 是 OTel 组件壳：New 装配全局 provider 并装饰日志面（tracing
// 信号 + 日志链路注入），logs_enabled 时另建 OTLP 日志导出管线并经
// LogCore 供装配点组合进 zapc。Start 输出可见信号，Stop 在停机预算内
// flush 未发送的 span 与日志。
type Tracer struct {
	cfg      Config
	res      *resource.Resource
	once     sync.Once
	flush    func(context.Context) error // tracing provider Shutdown，幂等由 once 保证
	logFlush func(context.Context) error // 日志 provider Shutdown（nil = 未启用）
	logCore  zapcore.Core                // OTLP 日志导出桥（nil = 未启用）
}

// Option 构造选项。
type Option func(*options)

type options struct {
	service string // resource 属性 service.name
	env     string // resource 属性 deployment.environment.name
}

// WithService 设置 span 与日志的 OTel 资源标识（service.name 与
// deployment.environment.name，对齐 OTel semantic conventions）。
// 模板装配点从应用元数据传入：WithService(meta.Name, meta.Env)。
// 缺省不设置——记录仍可用，但聚合侧无法区分服务归属。
func WithService(name, env string) Option {
	return func(o *options) { o.service, o.env = name, env }
}

// New 构造即装配：创建 TracerProvider（endpoint 非空时挂 OTLP 导出器）
// 并安装为全局，随后装饰日志面——此后动态读 DefaultLogger() 的日志输出
// 在 ctx 携带有效 span 时自动附加 trace_id/span_id。logs_enabled 时另建
// OTLP 日志导出管线（LogCore 组合方式见 README）。构造失败即未启动，
// 无资源需要清理。
func New(cfg Config, opts ...Option) (*Tracer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	res := buildResource(o)
	tp, err := newTracerProvider(cfg, res)
	if err != nil {
		return nil, err
	}
	otel.SetTracerProvider(tp)
	installLogTrace()
	t := &Tracer{cfg: cfg, res: res, flush: tp.Shutdown}
	if cfg.LogsEnabled {
		provider, core, err := buildLogCore(context.Background(), cfg, res)
		if err != nil {
			_ = tp.Shutdown(context.Background()) // 已建 provider 就地回收，失败即未启动
			return nil, err
		}
		t.logFlush = provider.Shutdown
		t.logCore = core
	}
	return t, nil
}

// LogCore 返回 OTLP 日志导出的 zap core（logs_enabled 时非 nil）。
// 由装配点经 zapc.WithCore 并进 zap tee——依赖 zapc 后端；core 是 kit
// 构建参数，热更重建自动带上。停机顺序：zapc 先接线（后停止，Sync 把
// 在途记录推入 batch）、本组件先接线（后 flush），链路自然衔接。
// 未启用时返回 nil；zapc.WithCore(nil) 会被忽略。
func (t *Tracer) LogCore() zapcore.Core { return t.logCore }

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

// Stop 在停机预算内 flush 未发送的日志与 span（日志先于 tracing，
// 对齐参考实现的收尾次序）；幂等可重入，flush 失败降级为警告日志
// （不视作停机失败，对齐 zapc 的收尾语义）。
func (t *Tracer) Stop(ctx context.Context) error {
	t.once.Do(func() {
		if t.logFlush != nil {
			if err := t.logFlush(ctx); err != nil {
				observ.DefaultLogger().Log(ctx, slog.LevelWarn, "otelc_logs_flush_failed",
					slog.Any("error", err))
			}
		}
		if err := t.flush(ctx); err != nil {
			observ.DefaultLogger().Log(ctx, slog.LevelWarn, "otelc_tracer_flush_failed",
				slog.Any("error", err))
		}
	})
	return nil
}
