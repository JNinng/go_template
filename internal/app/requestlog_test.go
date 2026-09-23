package app

// 请求日志独立实例的装配行为：派生落点（zapc.path 同目录 req.log）、
// caller 关闭、等级随 zapc 节热更门控。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go_template/internal/runner"
)

func TestBindRequestLogger_DerivesAndFollowsHotReload(t *testing.T) {
	t.Chdir(t.TempDir()) // 派生路径相对工作目录，隔离到临时目录
	tr, _ := newUseTree(t, "zapc:\n  path: app.log\nhttpserver:\n  addr: 127.0.0.1:0\n")
	r := runner.New()

	accessLog, err := bindRequestLogger(tr, r)
	if err != nil {
		t.Fatal(err)
	}
	// sink 句柄须在 TempDir 清理前释放（Windows 文件锁）：StartAll 把
	// req-log 计入启动范围，StopAll 执行其停机钩子（Sync + Close）。
	if err := r.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.StopAll() }()

	// 派生落点：zapc.path（app.log）同目录的 req.log，构造即探针建文件
	if _, err := os.Stat("req.log"); err != nil {
		t.Fatalf("req.log must be created next to zapc.path: %v", err)
	}

	accessLog().Info("probe_initial")
	readReq := func() string {
		t.Helper()
		b, err := os.ReadFile("req.log")
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	got := readReq()
	if !strings.Contains(got, "probe_initial") {
		t.Fatalf("request log must reach req.log, got:\n%s", got)
	}
	if strings.Contains(got, "caller") {
		t.Fatalf("request log must not carry caller, got:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join("log", "req.log")); err == nil {
		t.Fatal("fallback path must not be used when zapc.path is set")
	}

	// 热更：zapc 节 level → error，请求日志门控跟随（Info 被拦）
	src := &pushSrc{fn: func(ctx context.Context, push func(map[string]any)) error {
		push(map[string]any{"zapc": map[string]any{"path": "app.log", "level": "error"}})
		<-ctx.Done()
		return nil
	}}
	if err := tr.Attach(src); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for i := 0; ; i++ {
		if time.Now().After(deadline) {
			t.Fatal("level gating did not follow zapc hot reload")
		}
		msg := fmt.Sprintf("gated_%d", i)
		accessLog().Info(msg) // 投递前会落盘；一旦生效，新消息不再出现
		if !strings.Contains(readReq(), msg) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
