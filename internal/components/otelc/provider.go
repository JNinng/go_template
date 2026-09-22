package otelc

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.27.0"
)

// newTracerProvider 组装 TracerProvider：恒建 provider（trace_id 生成
// 能力与 endpoint 无关），endpoint 非空才追加以批量 SpanProcessor 包装
// 的 OTLP 导出器（grpc/http 同地址，恒 insecure——本组件面向本地 /
// 内网 collector，TLS 与凭据不在范围）。
func newTracerProvider(cfg Config, res *resource.Resource) (*sdktrace.TracerProvider, error) {
	tpOpts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if cfg.Endpoint != "" {
		exporter, err := newExporter(context.Background(), cfg)
		if err != nil {
			return nil, err
		}
		tpOpts = append(tpOpts, sdktrace.WithBatcher(exporter))
	}
	return sdktrace.NewTracerProvider(tpOpts...), nil
}

// buildResource 构造 span 与日志共用的 OTel 资源：仅携带服务标识属性
// （与 resource.Default 的 schema URL 版本不同，不合并——合并即冲突）。
// 采样策略不配置——SDK 缺省 parent-based always-on。
func buildResource(o options) *resource.Resource {
	var attrs []attribute.KeyValue
	if o.service != "" {
		attrs = append(attrs, semconv.ServiceName(o.service))
	}
	if o.env != "" {
		attrs = append(attrs, semconv.DeploymentEnvironmentName(o.env))
	}
	if o.version != "" {
		attrs = append(attrs, semconv.ServiceVersion(o.version))
	}
	if o.instanceID != "" {
		attrs = append(attrs, semconv.ServiceInstanceID(o.instanceID))
	}
	if len(attrs) == 0 {
		return resource.Default()
	}
	return resource.NewWithAttributes(semconv.SchemaURL, attrs...)
}

// newExporter 按 protocol 创建 OTLP trace 导出器（恒 insecure）。
func newExporter(ctx context.Context, cfg Config) (sdktrace.SpanExporter, error) {
	switch cfg.Protocol {
	case "http":
		return otlptracehttp.New(ctx,
			otlptracehttp.WithEndpoint(cfg.Endpoint),
			otlptracehttp.WithInsecure(),
		)
	default: // Validate 已挡，grpc 为缺省路径
		return otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpoint(cfg.Endpoint),
			otlptracegrpc.WithInsecure(),
		)
	}
}
