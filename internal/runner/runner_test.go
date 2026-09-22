package runner

import (
	"context"
	"sync"
	"testing"
	"time"
)

// rec 记录生命周期钩子的触发顺序（线程安全）。
type rec struct {
	mu     sync.Mutex // 保护 events
	events []string   // 钩子事件时序，如 "start:a"、"stop:a"
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

var errStart = &startError{}

type startError struct{}

func (*startError) Error() string { return "boom" }

func TestStartAll_OrdersAndSkipsNil(t *testing.T) {
	r := New()
	rc := &rec{}
	r.Add("a", func(context.Context) error { rc.record("start:a"); return nil }, nil)
	r.Add("b", nil, nil) // start/stop 均为 nil → 跳过
	r.Add("c", func(context.Context) error { rc.record("start:c"); return nil }, nil)

	if err := r.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := rc.snapshot()
	if len(got) != 2 || got[0] != "start:a" || got[1] != "start:c" {
		t.Fatalf("start order wrong: %v", got)
	}
}

func TestStartAll_FailureRollsBackStarted(t *testing.T) {
	r := New()
	rc := &rec{}
	r.Add("a", func(context.Context) error { rc.record("start:a"); return nil },
		func(context.Context) error { rc.record("stop:a"); return nil })
	r.Add("b", func(context.Context) error { return errStart }, nil)
	r.Add("c", func(context.Context) error { rc.record("start:c"); return nil }, nil)

	if err := r.StartAll(context.Background()); err == nil {
		t.Fatal("start failure must return error")
	}
	got := rc.snapshot()
	if len(got) != 2 || got[0] != "start:a" || got[1] != "stop:a" {
		t.Fatalf("rollback wrong: %v (want start:a then stop:a, c never started)", got)
	}
}

func TestStartAll_NilStartEntryCountsForStop(t *testing.T) {
	// nil-start 条目（资源在装配期已建立，如日志文件钩子）也进入停止范围
	r := New()
	rc := &rec{}
	r.Add("hook", nil, func(context.Context) error { rc.record("stop:hook"); return nil })
	r.Add("svc", func(context.Context) error { rc.record("start:svc"); return nil },
		func(context.Context) error { rc.record("stop:svc"); return nil })

	if err := r.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.StopAll(); err != nil {
		t.Fatal(err)
	}
	got := rc.snapshot()
	want := []string{"start:svc", "stop:svc", "stop:hook"}
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v (hook registered before svc must stop last)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
}

func TestStopAll_ReverseOrderWithBudgetCtx(t *testing.T) {
	r := New()
	rc := &rec{}
	deadlines := map[string]time.Time{}
	mk := func(name string) func(context.Context) error {
		return func(ctx context.Context) error {
			rc.record("stop:" + name)
			d, ok := ctx.Deadline()
			if !ok {
				t.Errorf("stop %s: ctx has no deadline", name)
			}
			deadlines[name] = d
			return nil
		}
	}
	r.Add("a", nil, mk("a"))
	r.Add("b", nil, mk("b"))
	r.Add("c", nil, mk("c"))

	if err := r.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.StopAll(); err != nil {
		t.Fatal(err)
	}
	got := rc.snapshot()
	if len(got) != 3 || got[0] != "stop:c" || got[1] != "stop:b" || got[2] != "stop:a" {
		t.Fatalf("stop order wrong: %v", got)
	}
	now := time.Now()
	for name, d := range deadlines {
		if d.After(now.Add(defaultStepTimeout + time.Second)) {
			t.Errorf("stop %s: step deadline %v exceeds defaultStepTimeout", name, d)
		}
	}
}

// SetBudgets：合法取值生效、非法取值（非正、total < step）拒绝且不变更。
func TestSetBudgets(t *testing.T) {
	r := New()
	if r.stepTimeout != 15*time.Second || r.totalBudget != 30*time.Second {
		t.Fatalf("defaults = %s/%s, want 15s/30s", r.stepTimeout, r.totalBudget)
	}
	if err := r.SetBudgets(2*time.Second, 5*time.Second); err != nil {
		t.Fatalf("valid budgets rejected: %v", err)
	}
	if r.stepTimeout != 2*time.Second || r.totalBudget != 5*time.Second {
		t.Fatalf("budgets not applied: %s/%s", r.stepTimeout, r.totalBudget)
	}
	for _, bad := range [][2]time.Duration{{0, 5 * time.Second}, {1 * time.Second, 0}, {-1 * time.Second, 5 * time.Second}, {6 * time.Second, 5 * time.Second}} {
		if err := r.SetBudgets(bad[0], bad[1]); err == nil {
			t.Errorf("SetBudgets(%s, %s) must be rejected", bad[0], bad[1])
		}
		if r.stepTimeout != 2*time.Second || r.totalBudget != 5*time.Second {
			t.Fatalf("rejected call mutated state: %s/%s", r.stepTimeout, r.totalBudget)
		}
	}
}

func TestStopAll_ComponentHonorsCtxAndReportsError(t *testing.T) {
	r := New()
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

	if err := r.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	begin := time.Now()
	if err := r.StopAll(); err != nil {
		t.Fatal(err)
	}
	// stop 错误仅记日志不致命，全部条目仍被执行完
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Fatalf("shutdown should finish promptly, took %v", elapsed)
	}
}

func TestStopAll_IdempotentAfterRollback(t *testing.T) {
	// StartAll 失败已回滚；再 StopAll 不得重复执行 stop
	r := New()
	rc := &rec{}
	r.Add("a", func(context.Context) error { rc.record("start:a"); return nil },
		func(context.Context) error { rc.record("stop:a"); return nil })
	r.Add("b", func(context.Context) error { return errStart }, nil)

	if err := r.StartAll(context.Background()); err == nil {
		t.Fatal("want start failure")
	}
	if err := r.StopAll(); err != nil {
		t.Fatal(err)
	}
	got := rc.snapshot()
	if len(got) != 2 {
		t.Fatalf("stop must not run twice after rollback: %v", got)
	}
}

func TestStopAll_ZeroStarted(t *testing.T) {
	r := New()
	r.Add("a", nil, func(context.Context) error { t.Fatal("must not run"); return nil })
	if err := r.StopAll(); err != nil {
		t.Fatal(err)
	}
}

func TestNames(t *testing.T) {
	r := New()
	r.Add("a", nil, nil)
	r.Add("b", nil, nil)
	names := r.Names()
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("Names() = %v", names)
	}
}
