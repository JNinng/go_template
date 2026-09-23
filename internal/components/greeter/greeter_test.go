package greeter

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jninng/observ"
)

// syncBuffer 串行化并发读写：greeter goroutine 经 handler 写、测试
// goroutine 轮询读，裸 bytes.Buffer 会构成数据竞争。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) contains(sub []byte) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Contains(b.buf.Bytes(), sub)
}

func TestNew_Validation(t *testing.T) {
	if _, err := New(Config{Message: "", IntervalSec: 1}); err == nil {
		t.Fatal("empty message must fail construction")
	}
	if _, err := New(Config{Message: "hi", IntervalSec: 0}); err == nil {
		t.Fatal("non-positive interval must fail construction")
	}
}

func TestLifecycle_TicksThenStopsIdempotently(t *testing.T) {
	buf := &syncBuffer{}
	logger := observ.NewSlogLogger(slog.New(slog.NewTextHandler(buf, nil)))

	h, err := New(Config{Message: "hi", IntervalSec: 1}, WithLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.Start(ctx); err != nil {
		t.Fatal(err)
	}

	// 1s 周期：等待首个 tick 落盘（留足裕量防慢机）
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !buf.contains([]byte("greeter_tick")) {
		time.Sleep(50 * time.Millisecond)
	}
	if !buf.contains([]byte("greeter_tick")) {
		t.Fatal("no tick observed within 3s")
	}

	if err := h.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.Stop(ctx); err != nil {
		t.Fatal("Stop must be idempotent")
	}
}

func TestApplyConfig_ValidationAndHotUpdate(t *testing.T) {
	h, err := New(Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ApplyConfig(Config{Message: "x", IntervalSec: 0}); err == nil {
		t.Fatal("non-positive interval must be rejected")
	}
	if err := h.ApplyConfig(Config{Message: "", IntervalSec: 5}); err == nil {
		t.Fatal("empty message must be rejected (same standard as New)")
	}
	if err := h.ApplyConfig(Config{Message: "updated", IntervalSec: 5}); err != nil {
		t.Fatal(err)
	}
	if got := h.current(); got.Message != "updated" || got.IntervalSec != 5 {
		t.Fatalf("ApplyConfig not applied: %+v", got)
	}
}

// TestSection_SelfDeclaration 钉住节名契约：组件自述与文档声明的节名
// 恒一致（字面量漂移在此暴露，而非运行期才被装配校验发现）。
func TestSection_SelfDeclaration(t *testing.T) {
	g := &Greeter{}
	if g.Section() != SectionName || SectionName != "greeter" {
		t.Fatalf("section self-declaration drifted: method=%q const=%q", g.Section(), SectionName)
	}
}
