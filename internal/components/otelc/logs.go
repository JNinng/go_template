package otelc

import (
	"context"
	"fmt"

	"go.opentelemetry.io/contrib/bridges/otelzap"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.uber.org/zap/zapcore"
)

// buildLogCore 构建 OTLP 日志导出管线并经 otelzap 桥为 zapcore.Core：
// exporter（gRPC/HTTP，恒 insecure，与 tracing 共用 endpoint/protocol）
// → BatchProcessor → LoggerProvider（复用 tracing 的 resource）。
// Core 塞进 zapc 的 tee 后，进入 zap 的每条日志自动出海（zap.L / kit /
// observ 三条调用面全覆盖）；出海流不受 zapc 级别门控（AtomicLevel 只
// 作用于文件/控制台 core），级别裁剪交给 collector 侧。
// 返回 provider，其 Shutdown 由组件 Stop 统一驱动（先于 tracing flush）。
func buildLogCore(ctx context.Context, cfg Config, res *resource.Resource) (*log.LoggerProvider, zapcore.Core, error) {
	var exporter log.Exporter
	var err error
	switch cfg.Protocol {
	case "http":
		exporter, err = otlploghttp.New(ctx,
			otlploghttp.WithEndpoint(cfg.Endpoint),
			otlploghttp.WithInsecure(),
		)
	default: // Validate 已挡，grpc 为缺省路径
		exporter, err = otlploggrpc.New(ctx,
			otlploggrpc.WithEndpoint(cfg.Endpoint),
			otlploggrpc.WithInsecure(),
		)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("otelc: create log exporter: %w", err)
	}
	provider := log.NewLoggerProvider(
		log.WithProcessor(log.NewBatchProcessor(exporter)),
		log.WithResource(res),
	)
	core := otelzap.NewCore("otel-log", otelzap.WithLoggerProvider(provider))
	return provider, core, nil
}
