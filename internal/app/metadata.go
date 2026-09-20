package app

import (
	"context"
	"fmt"
	"log/slog"

	"go_template/internal/config"

	"github.com/jninng/observ"
)

// Version 由构建期注入：
//
//	go build -ldflags "-X '<module>/internal/app.Version=v1.2.3'" ./cmd/app
//
// Version 不进配置文件。
var Version = "dev"

// Meta 是模板自持的 app 节定义（应用元数据是纯数据，由装配点显式传参消费）。
type Meta struct {
	Name string `yaml:"name"` // 应用名，必填非空（缺失或为空 → 启动 fail-fast）
	Env  string `yaml:"env"`  // 运行环境声明值，供下游消费（启动日志、可观测资源、注册分组）
}

// Default 是 app 节解码基座（无默认值：name 必填）。
func Default() Meta { return Meta{} }

// loadMeta 解析应用元数据并计算生效 env：
// --env / APP_ENV（文件选择输入）> app.env 声明值 > 空。
// 前者是唯一有权选择多环境文件的输入，避免"配置里改 env 换文件"的循环依赖；
// 后者仅为声明。
func loadMeta(t *config.Tree, envInput string) (Meta, string, error) {
	m, err := config.Decode(t, "app", Default())
	if err != nil {
		return Meta{}, "", err
	}
	if m.Name == "" {
		return Meta{}, "", fmt.Errorf("app: name is required and empty (check app.name in config)")
	}
	eff := envInput
	if eff == "" {
		eff = m.Env
	}
	return m, eff, nil
}

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
	observ.DefaultLogger().Log(ctx, slog.LevelInfo, "service_started",
		slog.String("app_name", a.appName),
		slog.String("app_env", a.appEnv),
		slog.String("app_version", a.version))
	return nil
}
