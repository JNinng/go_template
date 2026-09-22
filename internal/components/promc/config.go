package promc

import (
	"fmt"
	"strings"
)

// Config 是 promc 配置节（节名 "promc"，与包名一致）。全部字段为标量；
// 本组件不热更（不实现 ApplyConfig），变更重启生效。
type Config struct {
	Addr        string `yaml:"addr"`         // 独立监听地址；缺省空 = 不自起 server（handler 仍可挂到业务路由，见 MetricsHandler）
	MetricsPath string `yaml:"metrics_path"` // 指标暴露路径；缺省 /metrics
	HealthPath  string `yaml:"health_path"`  // 健康检查路径；缺省 /health
}

// Default 返回默认值基座（初始解码用；本组件不热更，无重解码）。
// addr 缺省空——单端口形态（httpserver 挂载 MetricsHandler /
// HealthHandler）为模板缺省，需要独立端口显式配置。
func Default() Config {
	return Config{Addr: "", MetricsPath: "/metrics", HealthPath: "/health"}
}

// Validate 构造期拒绝标准：addr 可空（空 = 不自起 server）、路径以 /
// 起头、两路径互异。
func (c Config) Validate() error {
	for _, p := range []struct{ name, val string }{
		{"metrics_path", c.MetricsPath},
		{"health_path", c.HealthPath},
	} {
		if p.val == "" {
			return fmt.Errorf("promc: %s must not be empty", p.name)
		}
		if !strings.HasPrefix(p.val, "/") {
			return fmt.Errorf("promc: %s must start with %q, got %q", p.name, "/", p.val)
		}
	}
	if c.MetricsPath == c.HealthPath {
		return fmt.Errorf("promc: metrics_path and health_path must differ, both %q", c.MetricsPath)
	}
	return nil
}
