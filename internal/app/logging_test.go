package app

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go_template/internal/config"
	"go_template/internal/runner"

	"github.com/jninng/observ"
)

// logging 装配改动包级默认，测试须恢复。
func restoreLogging(t *testing.T) (func(), *slog.LevelVar) {
	t.Helper()
	prevSlog := slog.Default()
	prevObs := observ.SetDefaultLogger(observ.NoopLogger)
	return func() {
		slog.SetDefault(prevSlog)
		observ.SetDefaultLogger(prevObs)
	}, nil
}

func newLogTree(t *testing.T, level string) *config.Tree {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	content := "app:\n  name: demo\nlog:\n  level: " + level + "\n  format: text\n  output: stdout\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	tr, err := config.Load(p, "")
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// pushSrc 经 Attach 注入远程快照，驱动 log 节热更。
type pushSrc struct {
	fn func(ctx context.Context, push func(map[string]any)) error // Start 行为
}

func (s *pushSrc) Name() string { return "test" }
func (s *pushSrc) Start(ctx context.Context, push func(map[string]any)) error {
	return s.fn(ctx, push)
}

func TestSetupLogging_SetsBackend(t *testing.T) {
	restore, _ := restoreLogging(t)
	defer restore()

	tr := newLogTree(t, "info")
	r := runner.New()
	if err := setupLogging(tr, r); err != nil {
		t.Fatal(err)
	}
	l := observ.DefaultLogger()
	if !l.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("info should be enabled at level=info")
	}
	if l.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("debug should be disabled at level=info")
	}
}

func TestSetupLogging_InvalidLevelFailsFast(t *testing.T) {
	restore, _ := restoreLogging(t)
	defer restore()

	tr := newLogTree(t, "bogus")
	if err := setupLogging(tr, runner.New()); err == nil {
		t.Fatal("invalid level at startup must fail-fast")
	}
}

func TestSetupLogging_LevelHotReload(t *testing.T) {
	restore, _ := restoreLogging(t)
	defer restore()

	tr := newLogTree(t, "info")
	if err := setupLogging(tr, runner.New()); err != nil {
		t.Fatal(err)
	}
	if observ.DefaultLogger().Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("precondition: debug disabled")
	}

	// 热更 level=debug：改配置树 → Watch → 后端级别
	src := &pushSrc{fn: func(ctx context.Context, push func(map[string]any)) error {
		push(map[string]any{"log": map[string]any{
			"level": "debug", "format": "text", "output": "stdout",
		}})
		<-ctx.Done()
		return nil
	}}
	if err := tr.Attach(src); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if observ.DefaultLogger().Enabled(context.Background(), slog.LevelDebug) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("level hot reload to debug not effective")
}

func TestSetupLogging_HotReloadInvalidLevelKeepsOld(t *testing.T) {
	restore, _ := restoreLogging(t)
	defer restore()

	tr := newLogTree(t, "info")
	if err := setupLogging(tr, runner.New()); err != nil {
		t.Fatal(err)
	}

	src := &pushSrc{fn: func(ctx context.Context, push func(map[string]any)) error {
		push(map[string]any{"log": map[string]any{
			"level": "bogus", "format": "text", "output": "stdout",
		}})
		<-ctx.Done()
		return nil
	}}
	if err := tr.Attach(src); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // 等投递
	l := observ.DefaultLogger()
	if !l.Enabled(context.Background(), slog.LevelInfo) || l.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("invalid hot-reload level must keep previous level")
	}
}

func TestSetupLogging_FileOutputRegistersCloseHook(t *testing.T) {
	restore, _ := restoreLogging(t)
	defer restore()

	dir := t.TempDir()
	logFile := filepath.Join(dir, "app.log")
	base := filepath.Join(dir, "config.yaml")
	content := "app:\n  name: demo\nlog:\n  level: info\n  format: text\n  output: " + filepath.ToSlash(logFile) + "\n"
	if err := os.WriteFile(base, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	tr, err := config.Load(base, "")
	if err != nil {
		t.Fatal(err)
	}
	r := runner.New()
	if err := setupLogging(tr, r); err != nil {
		t.Fatal(err)
	}
	if names := r.Names(); len(names) != 1 || names[0] != "log-close" {
		t.Fatalf("log-close hook not registered: %v", names)
	}
	// 全程起停：StopAll 执行关闭钩子后文件句柄释放（TempDir 清理可成功）
	if err := r.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.StopAll(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(logFile); err != nil {
		t.Fatalf("log file not created: %v", err)
	}
}

func TestParseLevel(t *testing.T) {
	for _, s := range []string{"debug", "info", "warn", "error"} {
		if _, err := parseLevel(s); err != nil {
			t.Errorf("parseLevel(%q) err: %v", s, err)
		}
	}
	if _, err := parseLevel("bogus"); err == nil {
		t.Error("parseLevel(bogus) should fail")
	}
}
