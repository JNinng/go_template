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
	"go.opentelemetry.io/otel/propagation"
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
	service    string // resource 属性 service.name
	env        string // resource 属性 deployment.environment.name
	version    string // resource 属性 service.version
	instanceID string // resource 属性 service.instance.id
}

// WithService 设置 span 与日志的 OTel 资源标识（service.name /
// deployment.environment.name / service.version，对齐 OTel semantic
// conventions）。模板装配点从应用元数据与构建元数据传入：
// WithService(meta.Name, meta.Env, version.Version)。缺省不设置——
// 记录仍可用，但聚合侧无法区分服务归属。
func WithService(name, env, ver string) Option {
	return func(o *options) { o.service, o.env, o.version = name, env, ver }
}

// WithInstanceID 设置 resource 属性 service.instance.id（对齐 OTel
// semantic conventions）：trace 数据由此与响应头 X-Instance-IDs、日志
// 定位到同一实例。值由装配点传入（httpserver.InstanceID 的产物，
// 单一事实源）。
func WithInstanceID(id string) Option {
	return func(o *options) { o.instanceID = id }
}

// New 构造即装配：创建 TracerProvider（endpoint 非空时挂 OTLP 导出器）
// 并安装为全局，随后装饰日志面——此后动态读 DefaultLogger() 的日志输出
// 在 ctx 携带有效 span 时自动附加 trace_id/span_id（装饰实现 Rebind
// 协议，在日志后端接管与热更重建后保持有效）。logs_enabled 时另建
// OTLP 日志导出管线（LogCore 组合方式见 README）。构造失败即未启动、
// 已建 provider 就地回收；此前的全局指派随引导失败进程退出，无实际影响。
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
	// otel ≥1.33 的全局传播器缺省为 noop——不显式装 W3C 的话，
	// traceparent 头的提取（服务间串联）会静默失效。与 provider 同属
	// 全局装配，在此一并安装。
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
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
// 构建参数，热更重建自动带上。停机顺序：本组件先接线、zapc 后接线，
// 逆序停机即 zapc 先停（Sync 只刷本地 sink——记录在写入时已同步入队
// batch）、本组件后停 flush 出海，链路自然衔接。
// 未启用时返回 nil；zapc.WithCore(nil) 会被忽略。
func (t *Tracer) LogCore() zapcore.Core { return t.logCore }

// Start 输出启动信号后立即返回（provider 在构造期已生效，无后台
// goroutine）；导出属批量异步，由 SDK 自行驱动。
// Section 返回本组件的配置节名（统一节名获取接口，恒返回 SectionName）。
func (t *Tracer) Section() string { return SectionName }

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
