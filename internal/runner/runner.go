// Package runner 是运行器：模板唯一的生命周期机制——顺序启动、逆序停止、
// 信号、停机预算（docs/DESIGN.md §10）。独立于装配层：它不认识任何具体
// 组件，只登记并执行启停函数。
package runner

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/jninng/observ"
)

const (
	stepTimeout = 5 * time.Second  // 单组件停止预算
	totalBudget = 10 * time.Second // 停机总预算
)

// entry 是一个已注册组件的启停对。
type entry struct {
	name  string                      // 组件名（装配时给定，用于日志与错误归因）
	start func(context.Context) error // 启动钩子，nil 表示跳过
	stop  func(context.Context) error // 停止钩子（须幂等），nil 表示跳过
}

// Runner 按注册顺序启动、逆序停止所辖组件；信号与停机预算由其统一管理。
type Runner struct {
	entries []entry // 注册序即启动序，逆序即停止序
	started int     // 已成功启动的组件数（StopAll 的停止范围）
}

// New 创建空运行器。
func New() *Runner { return &Runner{} }

// Add 注册一对生命周期；start / stop 均可为 nil（nil 跳过）。
// 注册顺序即启动顺序，逆序即停止顺序。
func (r *Runner) Add(name string, start, stop func(context.Context) error) {
	r.entries = append(r.entries, entry{name, start, stop})
}

// Names 返回已注册组件名（注册顺序）；诊断与测试用。
func (r *Runner) Names() []string {
	names := make([]string, len(r.entries))
	for i, e := range r.entries {
		names[i] = e.name
	}
	return names
}

// StartAll 顺序启动全部组件；任一失败即逆序停止已启动者并返回错误
// （fail-fast 点）。Run 的启动半程，供需要手动控制生命周期的场景
// （一次性命令、嵌入其它进程管理器、测试）；常规入口是 Run。
func (r *Runner) StartAll(ctx context.Context) error {
	r.started = 0
	for _, e := range r.entries {
		if e.start != nil {
			if err := e.start(ctx); err != nil {
				_ = r.stopStarted()
				r.started = 0 // 已回滚：后续 StopAll 不再重复执行
				return fmt.Errorf("start %s: %w", e.name, err)
			}
		}
		// nil-start 条目也计入停止范围：其资源在装配期已建立
		//（如日志文件钩子），停机时同样需要释放
		r.started++
	}
	return nil
}

// StopAll 逆序停止已启动组件：单步预算 stepTimeout、总预算 totalBudget。
// Stop 错误仅记日志（技术故障 Error）不中断流程；预算耗尽即强杀
// （退出码 1）。幂等：未启动任何组件时为空操作。
func (r *Runner) StopAll() error {
	err := r.stopStarted()
	r.started = 0
	return err
}

// Run 阻塞执行：安装信号处理 → 顺序启动 → 等待信号/取消 → 逆序停机。
// 返回 nil 即优雅退出（退出码 0）；启动失败返回错误（退出码 1）。
func (r *Runner) Run() error {
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

	if err := r.StartAll(ctx); err != nil {
		return err
	}

	<-ctx.Done()
	return r.StopAll()
}

// stopStarted 逆序停止前 started 个已启动组件（不重置计数）。
func (r *Runner) stopStarted() error {
	deadline := time.Now().Add(totalBudget)
	for i := r.started - 1; i >= 0; i-- {
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

// logError 记技术故障（Error）：自动附加堆栈，只在最底层打一次。
func logError(msg string, attrs ...slog.Attr) {
	attrs = append(attrs, slog.String("stack", string(debug.Stack())))
	observ.DefaultLogger().Log(slog.LevelError, msg, attrs...)
}
