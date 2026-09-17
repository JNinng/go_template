package app

import (
	"context"
	"sync"
	"testing"
	"time"
)

// rec 记录 runner 生命周期钩子的触发顺序（线程安全）。
type rec struct {
	mu      sync.Mutex // 保护 events
	events  []string   // 钩子事件时序，如 "start:a"、"stop:a"
	started []string   // 已启动组件名（未使用于断言，保留可读性）
}

func (r *rec) record(e string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *rec) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func TestStartAll_OrdersAndRecords(t *testing.T) {
	r := new(runner)
	rc := &rec{}
	r.Add("a", func(context.Context) error { rc.record("start:a"); return nil }, func(context.Context) error { return nil })
	r.Add("b", nil, nil) // start/stop 均为 nil → 跳过
	r.Add("c", func(context.Context) error { rc.record("start:c"); return nil }, func(context.Context) error { return nil })

	started, err := r.startAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if started != 2 {
		t.Fatalf("started = %d, want 2 (nil start skipped)", started)
	}
	got := rc.snapshot()
	if len(got) != 2 || got[0] != "start:a" || got[1] != "start:c" {
		t.Fatalf("start order wrong: %v", got)
	}
}

func TestStartAll_FailureRollsBackStarted(t *testing.T) {
	r := new(runner)
	rc := &rec{}
	r.Add("a", func(context.Context) error { rc.record("start:a"); return nil },
		func(context.Context) error { rc.record("stop:a"); return nil })
	r.Add("b", func(context.Context) error { return errStart }, nil)
	r.Add("c", func(context.Context) error { rc.record("start:c"); return nil }, nil)

	if _, err := r.startAll(context.Background()); err == nil {
		t.Fatal("start failure must return error")
	}
	got := rc.snapshot()
	if len(got) != 2 || got[0] != "start:a" || got[1] != "stop:a" {
		t.Fatalf("rollback wrong: %v (want start:a then stop:a, c never started)", got)
	}
}

var errStart = &startError{}

type startError struct{}

func (*startError) Error() string { return "boom" }

func TestShutdown_ReverseOrderWithBudgetCtx(t *testing.T) {
	r := new(runner)
	rc := &rec{}
	var gotDeadline = map[string]time.Time{}
	mk := func(name string) func(context.Context) error {
		return func(ctx context.Context) error {
			rc.record("stop:" + name)
			d, ok := ctx.Deadline()
			if !ok {
				t.Errorf("stop %s: ctx has no deadline", name)
			}
			gotDeadline[name] = d
			return nil
		}
	}
	r.Add("a", nil, mk("a"))
	r.Add("b", nil, mk("b"))
	r.Add("c", nil, mk("c"))

	if err := r.shutdown(3); err != nil {
		t.Fatal(err)
	}
	got := rc.snapshot()
	if len(got) != 3 || got[0] != "stop:c" || got[1] != "stop:b" || got[2] != "stop:a" {
		t.Fatalf("stop order wrong: %v", got)
	}
	// 单步预算 ≤ 5s，且不超过总预算
	now := time.Now()
	for name, d := range gotDeadline {
		if d.After(now.Add(stepTimeout + time.Second)) {
			t.Errorf("stop %s: step deadline %v exceeds stepTimeout", name, d)
		}
		if d.After(now.Add(totalBudget + time.Second)) {
			t.Errorf("stop %s: step deadline %v exceeds totalBudget", name, d)
		}
	}
}

func TestShutdown_ComponentHonorsCtxAndReportsError(t *testing.T) {
	r := new(runner)
	r.Add("slow", nil, func(ctx context.Context) error {
		// 契约：超时由组件自行截断返回——不等满单步预算，100ms 主动让出
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
			return context.DeadlineExceeded
		}
	})
	r.Add("bad", nil, func(context.Context) error { return errStart })

	begin := time.Now()
	if err := r.shutdown(2); err != nil {
		t.Fatal(err)
	}
	// stop 错误仅记日志不致命，全部条目仍被执行完
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Fatalf("shutdown should finish promptly, took %v", elapsed)
	}
}

func TestShutdown_ZeroStarted(t *testing.T) {
	r := new(runner)
	r.Add("a", nil, func(context.Context) error { t.Fatal("must not run"); return nil })
	if err := r.shutdown(0); err != nil {
		t.Fatal(err)
	}
}
