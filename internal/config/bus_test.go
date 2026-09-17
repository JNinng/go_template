package config

import (
	"sync"
	"testing"
	"time"
)

// watchRec 记录 Watch 回调收到的全部值（线程安全）。
type watchRec struct {
	mu     sync.Mutex // 保护 values
	values []demoCfg  // 历次收到的配置
	ch     chan demoCfg // 供 select 等待的信号通道
}

func newWatchRec() *watchRec {
	return &watchRec{ch: make(chan demoCfg, 64)}
}

func (r *watchRec) apply(c demoCfg) error {
	r.mu.Lock()
	r.values = append(r.values, c)
	r.mu.Unlock()
	r.ch <- c
	return nil
}

func (r *watchRec) last() demoCfg {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.values) == 0 {
		return demoCfg{}
	}
	return r.values[len(r.values)-1]
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func newWatchTree(t *testing.T, section string) *Tree {
	t.Helper()
	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml",
		section+":\n  message: initial\n  interval_seconds: 1\n")
	tr, err := Load(base, "")
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestWatch_ConvergenceFirstCall(t *testing.T) {
	tr := newWatchTree(t, "demo")
	rec := newWatchRec()
	cancel := Watch(tr, "demo", demoCfg{Msg: "hello", N: 10}, rec.apply)
	defer cancel()

	select {
	case c := <-rec.ch:
		if c.Msg != "initial" || c.N != 1 {
			t.Fatalf("convergence first call got %+v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no convergence first call")
	}
}

func TestWatch_SectionChangeAndRemoval(t *testing.T) {
	// 文件层不含 demo 节：节的出现与消失完全由远程层驱动
	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml", "other:\n  a: 1\n")
	tr, err := Load(base, "")
	if err != nil {
		t.Fatal(err)
	}
	rec := newWatchRec()
	cancel := Watch(tr, "demo", demoCfg{Msg: "hello", N: 10}, rec.apply)
	defer cancel()
	<-rec.ch // 收敛首调：节缺失 → base

	tr.applyRemote(map[string]any{"demo": map[string]any{"message": "remote", "interval_seconds": 2}})
	select {
	case c := <-rec.ch:
		if c.Msg != "remote" || c.N != 2 {
			t.Fatalf("update got %+v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no update delivered")
	}

	// 节从所有层消失 = 变更回默认值（以 base 调用）
	tr.applyRemote(map[string]any{})
	select {
	case c := <-rec.ch:
		if c.Msg != "hello" || c.N != 10 {
			t.Fatalf("removal should deliver base defaults, got %+v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("removal not delivered")
	}
}

func TestWatch_DecodeFailureKeepsLastValid(t *testing.T) {
	tr := newWatchTree(t, "demo")
	rec := newWatchRec()
	cancel := Watch(tr, "demo", demoCfg{}, rec.apply)
	defer cancel()
	<-rec.ch // 收敛首调

	before := len(rec.snapshot())
	tr.applyRemote(map[string]any{"demo": map[string]any{"unknown_key": 1}})
	// 解码失败：丢弃本次、无回调
	time.Sleep(300 * time.Millisecond)
	if got := len(rec.snapshot()); got != before {
		t.Fatalf("decode failure must be dropped, callbacks %d -> %d", before, got)
	}
	if rec.last().Msg != "initial" {
		t.Fatalf("last valid value lost: %+v", rec.last())
	}
}

func (r *watchRec) snapshot() []demoCfg {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]demoCfg(nil), r.values...)
}

func TestWatch_ApplyErrorAndPanicAreIsolated(t *testing.T) {
	tr := newWatchTree(t, "demo")

	errCount := 0
	cancelErr := Watch(tr, "demo", demoCfg{}, func(demoCfg) error {
		errCount++
		return errBoom
	})
	defer cancelErr()

	panicCount := 0
	cancelPanic := Watch(tr, "demo", demoCfg{}, func(demoCfg) error {
		panicCount++
		panic("apply panicked")
	})
	defer cancelPanic()

	time.Sleep(300 * time.Millisecond) // 等收敛首调各执行一次
	if errCount != 1 {
		t.Fatalf("apply error callback ran %d times, want 1", errCount)
	}
	if panicCount != 1 {
		t.Fatalf("panicking callback ran %d times, want 1 (recovered)", panicCount)
	}

	// 进程未死：后续变更仍投递
	rec := newWatchRec()
	cancel3 := Watch(tr, "demo", demoCfg{}, rec.apply)
	defer cancel3()
	tr.applyRemote(map[string]any{"demo": map[string]any{"message": "still-alive"}})
	select {
	case c := <-rec.ch:
		if c.Msg != "still-alive" {
			t.Fatalf("got %+v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bus dead after panic/error")
	}
}

var errBoom = &boomError{}

type boomError struct{}

func (*boomError) Error() string { return "boom" }

func TestWatch_CancelStopsDelivery(t *testing.T) {
	tr := newWatchTree(t, "demo")
	rec := newWatchRec()
	cancel := Watch(tr, "demo", demoCfg{}, rec.apply)
	<-rec.ch // 收敛首调
	before := len(rec.snapshot())

	cancel()

	tr.applyRemote(map[string]any{"demo": map[string]any{"message": "after-cancel"}})
	time.Sleep(300 * time.Millisecond)
	if got := len(rec.snapshot()); got != before {
		t.Fatal("callback delivered after cancel")
	}

	// cancel 可重入
	cancel()
}

func TestWatch_BurstCoalescesToFinalState(t *testing.T) {
	tr := newWatchTree(t, "demo")
	rec := newWatchRec()
	cancel := Watch(tr, "demo", demoCfg{}, rec.apply)
	defer cancel()
	<-rec.ch // 收敛首调

	for i := 1; i <= 50; i++ {
		tr.applyRemote(map[string]any{
			"demo": map[string]any{"message": "burst", "interval_seconds": i},
		})
	}
	// 最终状态恒等于 Raw 读到的状态
	waitFor(t, 2*time.Second, func() bool { return rec.last().N == 50 })
	if rec.last().Msg != "burst" {
		t.Fatalf("final state mismatch: %+v", rec.last())
	}
}
