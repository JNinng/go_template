package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	stepTimeout = 5 * time.Second  // 单组件停止预算
	totalBudget = 10 * time.Second // 停机总预算
)

type entry struct {
	name  string                      // 组件名（装配点注册时给定，用于日志与错误归因）
	start func(context.Context) error // 启动钩子，nil 表示跳过
	stop  func(context.Context) error // 停止钩子（须幂等），nil 表示跳过
}

// runner 按注册顺序启动、逆序停止所辖组件；信号与停机预算由其统一管理。
type runner struct{ entries []entry } // entries：注册序即启动序，逆序即停止序

// Add 注册一对生命周期；start / stop 均可为 nil（nil 跳过）。
// 注册顺序即启动顺序，逆序即停止顺序。
func (r *runner) Add(name string, start, stop func(context.Context) error) {
	r.entries = append(r.entries, entry{name, start, stop})
}

// Run 阻塞执行：安装信号处理 → 顺序启动 → 等待信号/取消 → 逆序停机。
// 返回 nil 即优雅退出（退出码 0）；启动失败返回错误（退出码 1）。
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

// shutdown 逆序停止前 n 个已启动组件：单步预算 stepTimeout、总预算 totalBudget。
// Stop 错误仅记日志（技术故障 Error）不中断流程；预算耗尽即强杀（退出码 1）。
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
			logError("component_stop_failed",
				slog.String("component_name", e.name), slog.Any("error", err))
		}
		if time.Now().After(deadline) && i > 0 {
			logError("shutdown_budget_exhausted",
				slog.Int("remaining_components", i))
			os.Exit(1)
		}
	}
	return nil
}
