// Package app 是装配层：启动时序、组件接线、优雅停机。
// 时序（docs/DESIGN.md §4）：config.Load → setupLogging → meta → wire → runner.Run。
package app

import (
	"log/slog"

	"go_template/internal/config"

	"github.com/jninng/observ"
)

// Run 是模板唯一的启动时序。任一步失败即引导失败：错误上抛，
// 由命令层直写 stderr、以退出码 1 终止（此时日志可能未就绪）。
func Run(configPath, env, logLevel string) error {
	var overrides []config.Override
	if logLevel != "" {
		overrides = append(overrides, config.Override{Key: "log.level", Value: logLevel})
	}

	t, err := config.Load(configPath, env, overrides...)
	if err != nil {
		return err
	}

	r := new(runner)
	if err := setupLogging(t, r); err != nil {
		return err
	}

	meta, effEnv, err := loadMeta(t, env)
	if err != nil {
		return err
	}
	observ.DefaultLogger().Log(slog.LevelInfo, "service started",
		slog.String("name", meta.Name),
		slog.String("env", effEnv),
		slog.String("version", Version))

	if err := wire(t, r); err != nil {
		return err
	}
	return r.Run()
}
