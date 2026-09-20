// Package biz 是占位业务组件的家：项目落地后，以真实业务组件替换本包
// 内容（保持组件约定即可，装配形态不变——internal/app/biz.go 的接线无需
// 重写，换节名与构造参数即可）。
package biz

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jninng/observ"
)

// Config 是占位业务组件的配置节（节名 "biz"，与包名一致）。
type Config struct {
	Message string `yaml:"message"` // 启动时输出的内容
}

// Default 返回默认值基座（初始解码与热更重解码共用；本组件不订阅热更）。
func Default() Config { return Config{Message: "hello from biz"} }

// Option 构造选项。
type Option func(*Hello)

// WithLogger 显式注入日志面（保留给测试捕获；缺省构造期快照默认——
// 构造发生在装配点、晚于后端设置，快照即正确后端）。
func WithLogger(l observ.Logger) Option {
	return func(h *Hello) { h.logger = l }
}

// Hello 占位业务组件：启动时输出一句日志，演示组件约定与多组件组合；
// 项目落地后由真实业务组件替换。
type Hello struct {
	cfg    Config        // 构造期固定的配置（占位组件无热更能力，不实现 ApplyConfig）
	logger observ.Logger // 日志面（构造期快照）
}

// New 构造即校验：message 为空在此报错（fail-fast 点）；
// 失败即未启动，无任何需要清理的资源。
func New(cfg Config, opts ...Option) (*Hello, error) {
	if cfg.Message == "" {
		return nil, fmt.Errorf("biz: message must not be empty")
	}
	h := &Hello{cfg: cfg, logger: observ.DefaultLogger()}
	for _, o := range opts {
		o(h)
	}
	return h, nil
}

// Start 输出占位日志后立即返回（无后台 goroutine，直接可用）；
// ctx 取消是停机信号之一。
func (h *Hello) Start(ctx context.Context) error {
	h.logger.Log(slog.LevelInfo, "biz_started",
		slog.String("message", h.cfg.Message))
	return nil
}

// Stop 幂等无资源（占位组件无 goroutine、无连接）。
func (h *Hello) Stop(ctx context.Context) error {
	h.logger.Log(slog.LevelInfo, "biz_stopped")
	return nil
}
