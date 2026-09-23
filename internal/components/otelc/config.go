package otelc

import (
	"fmt"
)

// SectionName 是 otelc 的配置节名（与包名一致；装配点引用本常量
// 接线，AddComponent 校验与自述一致）。
const SectionName = "otelc"

// Config 是 otelc 配置节（节名 SectionName，与包名一致）。全部字段为标量；
// 本组件不热更（不实现 ApplyConfig），变更重启生效。
type Config struct {
	Endpoint    string `yaml:"endpoint"`     // OTLP collector 地址 host:port；空 = 不导出（仅本地 trace_id 生成与日志关联）
	Protocol    string `yaml:"protocol"`     // OTLP 传输协议：grpc（缺省）| http
	LogsEnabled bool   `yaml:"logs_enabled"` // OTLP 日志导出（依赖 zapc 后端，经 LogCore 组合，见 README）；缺省 false
}

// Default 返回默认值基座（初始解码用；本组件不热更，无重解码）。
func Default() Config {
	return Config{Endpoint: "", Protocol: "grpc", LogsEnabled: false}
}

// Validate 构造期拒绝标准：protocol 仅允许 grpc / http；日志导出没有
// "本地生成"退化语义（不同于 trace），启用即必须有 endpoint。
func (c Config) Validate() error {
	switch c.Protocol {
	case "grpc", "http":
	default:
		return fmt.Errorf("otelc: protocol must be %q or %q, got %q", "grpc", "http", c.Protocol)
	}
	if c.LogsEnabled && c.Endpoint == "" {
		return fmt.Errorf("otelc: logs_enabled requires a non-empty endpoint")
	}
	return nil
}
