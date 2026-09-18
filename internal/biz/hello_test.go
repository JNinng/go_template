package biz

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/jninng/observ"
)

// capture 构造捕获型日志面与其缓冲（测试断言输出内容）。
func capture() (observ.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return observ.NewSlogLogger(slog.New(slog.NewTextHandler(&buf, nil))), &buf
}

func TestNew_EmptyMessageRejected(t *testing.T) {
	if _, err := New(Config{Message: ""}); err == nil {
		t.Fatal("empty message must fail construction")
	}
}

func TestStart_LogsOneLine(t *testing.T) {
	logger, buf := capture()
	h, err := New(Config{Message: "hi-biz"}, WithLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatalf("Start must be infallible for valid config, got %v", err)
	}
	out := buf.String()
	for _, want := range []string{"msg=biz_started", "message=hi-biz"} {
		if !bytes.Contains([]byte(out), []byte(want)) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

func TestStop_IdempotentNoop(t *testing.T) {
	h, err := New(Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := h.Stop(ctx); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := h.Stop(ctx); err != nil {
		t.Fatalf("Stop must be idempotent, got %v", err)
	}
}
