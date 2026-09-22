package config

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"

	"github.com/jninng/observ"
)

// 热更总线契约（docs/DESIGN.md §8.6）：串行有序、逐订阅投递、收敛语义、
// 隔离（apply panic 被 recover）、取消后无在途且无后续回调。

type subscription struct {
	token    chan struct{} // 投递信号，缓冲深度 1：突发变更合并为最新值
	stopCh   chan struct{} // 关闭即通知订阅 goroutine 退出
	done     chan struct{} // 订阅 goroutine 退出时关闭，供 stop 等待在途回调结束
	stopOnce sync.Once     // 保证 stop 的幂等（可安全重入）
}

// newSubscription 构造一个处于待投递状态的订阅。
func newSubscription() *subscription {
	return &subscription{
		token:  make(chan struct{}, 1),
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
	}
}

// notify 非阻塞投递信号；缓冲已满即丢弃（订阅者处理时会读当前树，天然合并）。
func (s *subscription) notify() {
	select {
	case s.token <- struct{}{}:
	default:
	}
}

// stop 关停并等待在途回调完成：返回后该订阅无在途且无后续回调。
func (s *subscription) stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
	<-s.done
}

// register 把订阅纳入总线：此后每次树重建都会投递信号。
func (t *Tree) register(s *subscription) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.subs = append(t.subs, s)
}

// unregister 把已停止的订阅移出总线（幂等）。
func (t *Tree) unregister(s *subscription) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, cur := range t.subs {
		if cur == s {
			t.subs = append(t.subs[:i], t.subs[i+1:]...)
			return
		}
	}
}

// notifyAll 快照订阅集合并逐个非阻塞投递；不得持锁调用。
func (t *Tree) notifyAll() {
	t.mu.RLock()
	subs := make([]*subscription, len(t.subs))
	copy(subs, t.subs)
	t.mu.RUnlock()
	for _, s := range subs {
		s.notify()
	}
}

// Watch 节级热更订阅：建立时立即以当前值首调一次 apply（收敛语义，
// 封住 Decode 与订阅建立之间的竞态间隙），其后仅该节变更时调用。
// 节消失视为"变更回默认值"（以 base 调用）。解码失败（含未知键）或
// apply 返回 error → 记日志整体丢弃本次、保持上一有效值，进程不死。
func Watch[T any](t *Tree, section string, base T, apply func(T) error) (cancel func()) {
	s := newSubscription()
	// 投递路径不在业务 span 内：日志恒 Background，且每次动态读默认
	// logger（config 构造早于日志装配，快照会永久固定在 Noop）。
	deliver := func() {
		cfg, err := decodeSection(t, section, base)
		if err != nil {
			// 校验类异常（未知键/类型不符）：Warn，丢弃本次、保持上一有效值
			observ.DefaultLogger().Log(context.Background(), slog.LevelWarn, "config_watch_decode_error",
				slog.String("section", section), slog.Any("error", err))
			return
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					// panic 属意外缺陷：Error 兜底记录（含 panic 值与堆栈），进程不死
					observ.DefaultLogger().Log(context.Background(), slog.LevelError, "config_watch_apply_panicked",
						slog.String("section", section), slog.Any("panic_value", r),
						slog.String("stack", string(debug.Stack())))
				}
			}()
			if err := apply(cfg); err != nil {
				// apply 拒绝新值属组件侧校验：Warn，保持上一有效值
				observ.DefaultLogger().Log(context.Background(), slog.LevelWarn, "config_watch_apply_failed",
					slog.String("section", section), slog.Any("error", err))
			}
		}()
	}
	// 先注册后投递收敛信号：订阅 goroutine 处理时读当前树，
	// 注册与首调之间到达的变更不会丢失。
	t.register(s)
	go func() {
		defer close(s.done)
		defer t.unregister(s)
		s.notify()
		for {
			select {
			case <-s.stopCh:
				return
			case <-s.token:
				deliver()
			}
		}
	}()
	return func() { s.stop() }
}
