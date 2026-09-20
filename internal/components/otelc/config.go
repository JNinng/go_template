package otelc

import (
	"fmt"
)

// Config 是 otelc 配置节（节名 "otelc"，与包名一致）。全部字段为标量；
// 本组件不热更（不实现 ApplyConfig），变更重启生效。
type Config struct {
	Endpoint string `yaml:"endpoint"` // OTLP collector 地址 host:port；空 = 不导出（仅本地 trace_id 生成与日志关联）
	Protocol string `yaml:"protocol"` // OTLP 传输协议：grpc（缺省）| http
}

// Default 返回默认值基座（初始解码用；本组件不热更，无重解码）。
func Default() Config {
	return Config{Endpoint: "", Protocol: "grpc"}
}

// Validate 构造期拒绝标准：protocol 仅允许 grpc / http。
func (c Config) Validate() error {
	switch c.Protocol {
	case "grpc", "http":
	default:
		return fmt.Errorf("otelc: protocol must be %q or %q, got %q", "grpc", "http", c.Protocol)
	}
	return nil
}
