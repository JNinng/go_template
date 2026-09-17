package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime/debug"

	"go_template/internal/config"

	"github.com/jninng/observ"
)

// logConfig 是模板自持的 log 节定义。
type logConfig struct {
	Level  string `yaml:"level"`  // 级别：debug / info / warn / error（唯一热更字段，作用于后端级别、调用面无感）
	Format string `yaml:"format"` // 格式：text / json（重启生效）
	Output string `yaml:"output"` // 目标：stdout 或文件路径，仅追加（重启生效）
}

// logDefault 返回 log 节解码基座（零配置可用）。
func logDefault() logConfig {
	return logConfig{Level: "info", Format: "text", Output: "stdout"}
}

// setupLogging 设 observ 默认日志后端并订阅 log 节 level 热更。
// 换后端（如 zap）时这是唯一改动点。output 为文件时注册停机关闭钩子
// （stdlib handler 无缓冲，纯卫生，不丢数据）。
func setupLogging(t *config.Tree, r *runner) error {
	cfg, err := config.Decode(t, "log", logDefault())
	if err != nil {
		return err
	}
	lvl := new(slog.LevelVar)
	start, err := parseLevel(cfg.Level)
	if err != nil {
		return err // 启动期非法值 → fail-fast（热更期则保持旧值，见 Watch 回调）
	}
	lvl.Set(start)
	w, closeFn, err := outputWriter(cfg.Output)
	if err != nil {
		return err
	}
	h, err := buildHandler(cfg.Format, lvl, w)
	if err != nil {
		if closeFn != nil {
			closeFn()
		}
		return err
	}
	slog.SetDefault(slog.New(h))
	observ.SetDefaultLogger(observ.NewSlogLogger(slog.Default()))
	if closeFn != nil {
		// 先于一切组件注册 → 逆序停止时最后关闭，停机全程日志可用
		r.Add("log-close", nil, func(context.Context) error { return closeFn() })
	}

	config.Watch(t, "log", logDefault(), func(c logConfig) error {
		next, err := parseLevel(c.Level)
		if err != nil {
			return fmt.Errorf("keep level %q: %w", cfg.Level, err) // Warn，保持旧值
		}
		lvl.Set(next)
		if c.Format != cfg.Format || c.Output != cfg.Output {
			observ.DefaultLogger().Log(slog.LevelInfo, "log_config_restart_required",
				slog.String("format", c.Format), slog.String("output", c.Output))
		}
		return nil
	})
	return nil
}

// logError 记技术故障（Error）：自动附加堆栈，只在最底层打一次。
func logError(msg string, attrs ...slog.Attr) {
	attrs = append(attrs, slog.String("stack", string(debug.Stack())))
	observ.DefaultLogger().Log(slog.LevelError, msg, attrs...)
}

// parseLevel 把级别字符串解析为 slog.Level（严格四档 + slog 偏移语法）。
func parseLevel(s string) (slog.Level, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return 0, fmt.Errorf("log: invalid level %q", s)
	}
	return l, nil
}

// outputWriter 返回日志输出目标：stdout 或追加模式文件（不做轮转，轮转
// 归属部署侧或业务换入的方案——缺省日志链路保持零第三方依赖的边界）。
// 文件目标附带关闭函数（注册为停机钩子）。
func outputWriter(output string) (io.Writer, func() error, error) {
	if output == "" || output == "stdout" {
		return os.Stdout, nil, nil
	}
	f, err := os.OpenFile(output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("log: open output %q: %w", output, err)
	}
	return f, f.Close, nil
}

// buildHandler 按格式构造 slog handler，共享可热更的级别变量。
func buildHandler(format string, lvl *slog.LevelVar, w io.Writer) (slog.Handler, error) {
	opts := &slog.HandlerOptions{Level: lvl}
	switch format {
	case "text":
		return slog.NewTextHandler(w, opts), nil
	case "json":
		return slog.NewJSONHandler(w, opts), nil
	default:
		return nil, fmt.Errorf("log: unsupported format %q", format)
	}
}
