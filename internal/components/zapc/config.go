package zapc

import (
	"fmt"

	"go.uber.org/zap/zapcore"
)

// SectionName 是 zapc 的配置节名（与包名一致；装配点引用本常量
// 接线，AddComponent 校验与自述一致）。
const SectionName = "zapc"

// Config 是 zapc 配置节。全部字段为标量（可 == 比较）——热更按字段识别变更面。
type Config struct {
	Level        string `yaml:"level"`          // 日志级别：热更即时生效（仅调 AtomicLevel，实例不换）
	Format       string `yaml:"format"`         // 日志格式 (console/json)：热更重建生效
	Path         string `yaml:"path"`           // 日志文件路径，空则不写文件：热更重建生效
	MaxSize      int    `yaml:"max_size"`       // 单个日志文件最大大小 (MB)：热更重建生效
	MaxAge       int    `yaml:"max_age"`        // 日志文件保留天数：热更重建生效
	MaxBackups   int    `yaml:"max_backups"`    // 保留的日志文件数量：热更重建生效
	Compress     bool   `yaml:"compress"`       // 是否压缩历史日志：热更重建生效
	LogToConsole bool   `yaml:"log_to_console"` // 是否输出到控制台（stdout，固定非 json）：热更重建生效
}

// Default 返回默认值基座：纯控制台输出；轮转参数在 Path 非空时生效。
func Default() Config {
	return Config{
		Level:        "info",
		Format:       "console",
		Path:         "",
		MaxSize:      256,
		MaxAge:       60,
		MaxBackups:   120,
		Compress:     true,
		LogToConsole: true,
	}
}

// Validate 构造期与热更期共用同一拒绝标准。Path 能否打开不在配置层校验
// ——sink 在构建期打开，打不开即构造 / 热更失败。
func (c Config) Validate() error {
	if _, err := zapcore.ParseLevel(c.Level); err != nil {
		return fmt.Errorf("zapc: invalid level %q", c.Level)
	}
	switch c.Format {
	case "console", "json":
	default:
		return fmt.Errorf("zapc: format must be %q or %q, got %q", "console", "json", c.Format)
	}
	if c.Path == "" && !c.LogToConsole {
		return fmt.Errorf("zapc: no output destination: set path or enable log_to_console")
	}
	for _, p := range []struct {
		name string
		val  int
	}{{"max_size", c.MaxSize}, {"max_age", c.MaxAge}, {"max_backups", c.MaxBackups}} {
		if p.val <= 0 {
			return fmt.Errorf("zapc: %s must be > 0, got %d", p.name, p.val)
		}
	}
	return nil
}

// levelOnly 报告两个配置是否仅 Level 字段不同（其余全等）。
func levelOnly(a, b Config) bool {
	b.Level = a.Level
	return a == b
}
