// Package greeter 周期打印问候语：内置组件库的完整约定样例——
// Config/Default、New 构造校验、生命周期、ApplyConfig 热更、observ 注入。
// 直接 import 即可试用；深度定制时拷出到自己的包改造。
//
// 第三方依赖：无（仅 stdlib + observ）。
package greeter

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jninng/observ"
)

// Config 是 greeter 配置节。
type Config struct {
	Message     string `yaml:"message"`          // 问候内容，热更生效
	IntervalSec int    `yaml:"interval_seconds"` // 周期（秒），热更生效（下个周期起）
}

// Default 返回默认值基座（初始解码与热更重解码共用）。
func Default() Config {
	return Config{Message: "hello", IntervalSec: 10}
}

// Option 构造选项。
type Option func(*Greeter)

// WithLogger 显式注入日志面（测试捕获用；缺省构造期快照默认）。
func WithLogger(l observ.Logger) Option {
	return func(g *Greeter) { g.logger = l }
}

// Greeter 周期打印问候语。
type Greeter struct {
	mu     sync.Mutex    // 保护 cfg
	cfg    Config        // 当前生效配置
	logger observ.Logger // 日志面（构造于 wire，晚于后端设置，快照即正确后端）
	stop   chan struct{} // 幂等关闸
	done   chan struct{} // loop 回收信号
}

// New 构造即校验：配置不合法在此返回 error（fail-fast 点）。
// 失败即未启动，无任何需要清理的资源。
func New(cfg Config, opts ...Option) (*Greeter, error) {
	if cfg.Message == "" {
		return nil, fmt.Errorf("greeter: message must not be empty")
	}
	if cfg.IntervalSec <= 0 {
		return nil, fmt.Errorf("greeter: interval_seconds must be > 0, got %d", cfg.IntervalSec)
	}
	g := &Greeter{
		cfg:    cfg,
		logger: observ.DefaultLogger(), // 构造期快照
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	for _, o := range opts {
		o(g)
	}
	return g, nil
}

// Start 起 goroutine 后立即返回；返回 nil 即可用。ctx 取消是停机信号之一。
func (g *Greeter) Start(ctx context.Context) error {
	go g.loop(ctx)
	return nil
}

func (g *Greeter) loop(ctx context.Context) {
	defer close(g.done)
	for {
		t := time.NewTicker(time.Duration(g.current().IntervalSec) * time.Second)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-g.stop:
			t.Stop()
			return
		case <-t.C:
			c := g.current()
			g.logger.Log(slog.LevelInfo, "greeter_tick",
				slog.String("message", c.Message))
			t.Stop()
		}
	}
}

// Stop 幂等可重入；ctx 携带预算，超时自行截断。goroutine 归组件所有。
func (g *Greeter) Stop(ctx context.Context) error {
	select { // 幂等关闸
	case <-g.stop:
	default:
		close(g.stop)
	}
	select { // 等回收，尊重预算
	case <-g.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ApplyConfig 收敛语义：相同值必须无操作。可能在 Start 之前被调用
// （收敛首调），实现仅更新状态、不依赖运行时资源。
func (g *Greeter) ApplyConfig(cfg Config) error {
	if cfg.IntervalSec <= 0 {
		return fmt.Errorf("greeter: reject non-positive interval_seconds")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cfg = cfg
	return nil
}

func (g *Greeter) current() Config {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cfg
}
