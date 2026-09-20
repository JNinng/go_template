package app

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"go_template/internal/runner"

	"github.com/jninng/observ"
)

func TestAnnouncer_StartLogsServiceStarted(t *testing.T) {
	var buf bytes.Buffer
	prev := observ.SetDefaultLogger(observ.NewSlogLogger(slog.New(slog.NewTextHandler(&buf, nil))))
	defer observ.SetDefaultLogger(prev)

	a := newAnnouncer(Meta{Name: "demo"}, "prod")
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("announce Start must be infallible, got %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"msg=service_started",
		"app_name=demo",
		"app_env=prod",
		"app_version=" + Version, // 快照自包级 var（ldflags 注入点）
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

func TestSetupBiz_WiresPlaceholder(t *testing.T) {
	tr, _ := newUseTree(t, "biz:\n  message: hi-biz\n")
	r := runner.New()
	if err := setupBiz(tr, r, Meta{Name: "demo"}); err != nil {
		t.Fatal(err)
	}
	if names := r.Names(); len(names) != 2 || names[0] != "zapc" || names[1] != "biz" {
		t.Fatalf("placeholder not registered: %v", names)
	}
	// 占位组件起停回路（Start 记日志到 Noop，不产生输出）
	if err := r.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.StopAll(); err != nil {
		t.Fatal(err)
	}
}

func TestSetupBiz_MissingSectionUsesDefaults(t *testing.T) {
	tr, _ := newUseTree(t, "other:\n  a: 1\n")
	r := runner.New()
	if err := setupBiz(tr, r, Meta{Name: "demo"}); err != nil {
		t.Fatalf("missing biz section must fall back to defaults, got %v", err)
	}
	if len(r.Names()) != 2 {
		t.Fatalf("placeholder must register: %v", r.Names())
	}
}

func TestRun_MultiComponentOrder(t *testing.T) {
	// 组合顺序契约：announce 首个启动；业务组件随后；逆序停止跳过 nil stop
	tr, _ := newUseTree(t, "app:\n  name: demo\nbiz:\n  message: hi\n")
	r := runner.New()
	meta, eff, err := loadMeta(tr, "")
	if err != nil {
		t.Fatal(err)
	}
	r.Add("announce", newAnnouncer(meta, eff).Start, nil)
	if err := setupBiz(tr, r, meta); err != nil {
		t.Fatal(err)
	}
	if names := r.Names(); len(names) != 3 || names[0] != "announce" || names[1] != "zapc" || names[2] != "biz" {
		t.Fatalf("registration order wrong: %v", names)
	}
	if err := r.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.StopAll(); err != nil {
		t.Fatal(err)
	}
}
