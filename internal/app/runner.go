package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jninng/observ"
)

const (
	stepTimeout = 5 * time.Second  // 单组件停止预算
	totalBudget = 10 * time.Second // 停机总预算
)

type entry struct {
	name  string
	start func(context.Context) error
	stop  func(context.Context) error
}

type runner struct{ entries []entry }

// Add 注册一对生命周期；start / stop 均可为 nil（nil 跳过）。
// 注册顺序即启动顺序，逆序即停止顺序。
func (r *runner) Add(name string, start, stop func(context.Context) error) {
	r.entries = append(r.entries, entry{name, start, stop})
}

func (r *runner) Run() error {
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { // 第一个信号 → 优雅停机；第二个信号 → 立即强杀（防 Stop 卡死拖住进程）
		<-sigCh
		cancel()
		<-sigCh
		os.Exit(1)
	}()

	started, err := r.startAll(ctx)
	if err != nil {
		return err
	}

	<-ctx.Done()
	return r.shutdown(started)
}

// startAll 顺序启动；任一失败即逆序停止已启动者并返回错误（fail-fast 点）。
func (r *runner) startAll(ctx context.Context) (int, error) {
	started := 0
	for _, e := range r.entries {
		if e.start == nil {
			continue
		}
		if err := e.start(ctx); err != nil {
			_ = r.shutdown(started)
			return started, fmt.Errorf("start %s: %w", e.name, err)
		}
		started++
	}
	return started, nil
}

func (r *runner) shutdown(n int) error {
	deadline := time.Now().Add(totalBudget)
	for i := n - 1; i >= 0; i-- {
		e := r.entries[i]
		if e.stop == nil {
			continue
		}
		step := time.Now().Add(stepTimeout)
		if step.After(deadline) {
			step = deadline
		}
		stepCtx, cancel := context.WithDeadline(context.Background(), step)
		err := e.stop(stepCtx)
		cancel()
		if err != nil {
			observ.DefaultLogger().Log(slog.LevelWarn, "stop returned error",
				slog.String("component", e.name), slog.Any("err", err))
		}
		if time.Now().After(deadline) && i > 0 {
			observ.DefaultLogger().Log(slog.LevelError, "shutdown budget exhausted",
				slog.Int("remaining", i))
			os.Exit(1)
		}
	}
	return nil
}
