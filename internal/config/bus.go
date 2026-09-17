package config

import (
	"log/slog"
	"sync"
)

// 热更总线契约（docs/DESIGN.md §8.6）：串行有序、逐订阅投递、收敛语义、
// 隔离（apply panic 被 recover）、取消后无在途且无后续回调。

type subscription struct {
	token    chan struct{} // 缓冲深度 1：突发变更合并为最新值
	stopCh   chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

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

func (t *Tree) register(s *subscription) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.subs = append(t.subs, s)
}

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
	deliver := func() {
		cfg, err := decodeSection(t, section, base)
		if err != nil {
			t.logWarn("config watch decode failed, keeping last valid",
				slog.String("section", section), slog.Any("err", err))
			return
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.logWarn("config watch apply panicked",
						slog.String("section", section), slog.Any("panic", r))
				}
			}()
			if err := apply(cfg); err != nil {
				t.logWarn("config watch apply failed",
					slog.String("section", section), slog.Any("err", err))
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
