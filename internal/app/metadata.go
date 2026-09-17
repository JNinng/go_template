package app

import (
	"fmt"

	"go_template/internal/config"
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
