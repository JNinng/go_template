package greeter

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jninng/observ"
)

func TestNew_Validation(t *testing.T) {
	if _, err := New(Config{Message: "", IntervalSec: 1}); err == nil {
		t.Fatal("empty message must fail construction")
	}
	if _, err := New(Config{Message: "hi", IntervalSec: 0}); err == nil {
		t.Fatal("non-positive interval must fail construction")
	}
}

func TestLifecycle_TicksThenStopsIdempotently(t *testing.T) {
	var buf bytes.Buffer
	logger := observ.NewSlogLogger(slog.New(slog.NewTextHandler(&buf, nil)))

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
	for time.Now().Before(deadline) && !bytes.Contains(buf.Bytes(), []byte("greeter_tick")) {
		time.Sleep(50 * time.Millisecond)
	}
	if !bytes.Contains(buf.Bytes(), []byte("greeter_tick")) {
		t.Fatalf("no tick observed: %q", buf.String())
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
	if err := h.ApplyConfig(Config{Message: "updated", IntervalSec: 5}); err != nil {
		t.Fatal(err)
	}
	if got := h.current(); got.Message != "updated" || got.IntervalSec != 5 {
		t.Fatalf("ApplyConfig not applied: %+v", got)
	}
}
