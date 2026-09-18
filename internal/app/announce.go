package app

import (
	"context"
	"log/slog"

	"github.com/jninng/observ"
)

// announcer 是启动行组件：首个注册、首个启动的组件，输出模板运行的
// 最小可见信号（DESIGN §7）。自身无配置、无资源，Stop 为 nil（运行器
// 跳过）——与占位业务组件（internal/biz）共同演示多组件组合与启停顺序。
type announcer struct {
	appName string // 应用名（loadMeta 解析，必填非空）
	appEnv  string // 生效环境（--env / APP_ENV / 声明值的决议结果）
	version string // 构建期注入的版本快照
}

// newAnnouncer 构造启动行组件（无副作用，不连接）。
func newAnnouncer(meta Meta, effEnv string) *announcer {
	return &announcer{appName: meta.Name, appEnv: effEnv, version: Version}
}

// Start 输出启动行后立即返回；消息与字段 snake_case（§9 日志规范）。
func (a *announcer) Start(ctx context.Context) error {
	observ.DefaultLogger().Log(slog.LevelInfo, "service_started",
		slog.String("app_name", a.appName),
		slog.String("app_env", a.appEnv),
		slog.String("app_version", a.version))
	return nil
}
