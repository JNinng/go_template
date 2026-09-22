package app

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"go_template/internal/runner"
	"go_template/pkg/version"

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
		"app_version=" + version.Version, // 快照自 pkg/version（ldflags 注入点）
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

// bizTreeYaml 是测试用配置树：httpserver 绑 127.0.0.1:0（离线可跑，
// 不占用固定端口），biz 节喂占位组件。
const bizTreeYaml = "app:\n  name: demo\nhttpserver:\n  addr: 127.0.0.1:0\nbiz:\n  message: hi\n"

// wantBizNames 是 setupBiz 的注册顺序契约（书写顺序即依赖顺序：
// 可观测三件套在前，httpserver 消费它们的装配，占位业务在后）。
var wantBizNames = []string{"otelc", "zapc", "promc", "httpserver", "greeter", "biz"}

func TestSetupBiz_WiresPlaceholder(t *testing.T) {
	tr, _ := newUseTree(t, bizTreeYaml)
	r := runner.New()
	if err := setupBiz(tr, r, Meta{Name: "demo"}); err != nil {
		t.Fatal(err)
	}
	if names := r.Names(); !slices.Equal(names, wantBizNames) {
		t.Fatalf("registration order wrong: %v", names)
	}
	// 全链路起停回路（httpserver 绑随机端口、greeter 周期 goroutine 均正常回收）
	if err := r.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.StopAll(); err != nil {
		t.Fatal(err)
	}
}

func TestSetupBiz_MissingSectionUsesDefaults(t *testing.T) {
	tr, _ := newUseTree(t, "other:\n  a: 1\nhttpserver:\n  addr: 127.0.0.1:0\n")
	r := runner.New()
	if err := setupBiz(tr, r, Meta{Name: "demo"}); err != nil {
		t.Fatalf("missing sections must fall back to defaults, got %v", err)
	}
	if !slices.Equal(r.Names(), wantBizNames) {
		t.Fatalf("registration order wrong: %v", r.Names())
	}
}

func TestRun_MultiComponentOrder(t *testing.T) {
	// 组合顺序契约：announce 首个启动；业务组件随后；逆序停止跳过 nil stop
	tr, _ := newUseTree(t, bizTreeYaml)
	r := runner.New()
	meta, eff, err := loadMeta(tr, "")
	if err != nil {
		t.Fatal(err)
	}
	r.Add("announce", newAnnouncer(meta, eff).Start, nil)
	if err := setupBiz(tr, r, meta); err != nil {
		t.Fatal(err)
	}
	want := append([]string{"announce"}, wantBizNames...)
	if names := r.Names(); !slices.Equal(names, want) {
		t.Fatalf("registration order wrong: %v", names)
	}
	if err := r.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.StopAll(); err != nil {
		t.Fatal(err)
	}
}
