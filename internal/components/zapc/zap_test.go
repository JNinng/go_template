package zapc

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jninng/observ"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// testCfg 构造输出到指定文件、控制台关闭的基准配置。
func testCfg(path, level string) Config {
	c := Default()
	c.Path = path
	c.LogToConsole = false
	c.Level = level
	return c
}

// newForTest 构造输出到临时文件的组件实例，返回实例与输出路径。
func newForTest(t *testing.T, mutate func(*Config)) (*Log, string) {
	t.Helper()
	cfg := testCfg(filepath.Join(t.TempDir(), "app.log"), "info")
	if mutate != nil {
		mutate(&cfg)
	}
	z, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = z.Stop(context.Background()) })
	return z, cfg.Path
}

func content(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// wantContent 断言文件输出含子串。zap 写 sink 是同步调用，无需等待。
func wantContent(t *testing.T, path, substr string) {
	t.Helper()
	if got := content(t, path); !strings.Contains(got, substr) {
		t.Fatalf("%q not observed, got: %q", substr, got)
	}
}

func TestNew_Validation(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"bad level", Config{Level: "loud", Format: "console", MaxSize: 256, MaxAge: 60, MaxBackups: 120, LogToConsole: true}},
		{"bad format", Config{Level: "info", Format: "csv", MaxSize: 256, MaxAge: 60, MaxBackups: 120, LogToConsole: true}},
		{"no destination", Config{Level: "info", Format: "console", MaxSize: 256, MaxAge: 60, MaxBackups: 120}},
		{"zero max_size", Config{Level: "info", Format: "console", MaxSize: 0, MaxAge: 60, MaxBackups: 120, LogToConsole: true}},
		{"zero max_age", Config{Level: "info", Format: "console", MaxSize: 256, MaxAge: 0, MaxBackups: 120, LogToConsole: true}},
		{"zero max_backups", Config{Level: "info", Format: "console", MaxSize: 256, MaxAge: 60, MaxBackups: 0, LogToConsole: true}},
	}
	for _, c := range cases {
		if _, err := New(c.cfg); err == nil {
			t.Errorf("%s: must fail construction", c.name)
		}
	}
	if _, err := New(Default()); err != nil {
		t.Errorf("default config must construct: %v", err)
	}
}

func TestNew_CreatesMissingLogDir(t *testing.T) {
	z, path := newForTest(t, func(c *Config) {
		c.Path = filepath.Join(filepath.Dir(c.Path), "logs", "sub", "app.log")
	})
	z.kit.Info("into-fresh-dir")
	wantContent(t, path, "into-fresh-dir")
}

func TestLifecycle_WriteGlobalsAndSync(t *testing.T) {
	z, path := newForTest(t, nil)
	ctx := context.Background()
	if err := z.Start(ctx); err != nil {
		t.Fatal(err)
	}
	z.kit.Info("hello", zap.String("k", "v"))
	wantContent(t, path, "hello")

	// Start 装全局：zap.L() 直写同一 sink
	zap.L().Info("global-line")
	wantContent(t, path, "global-line")

	if err := z.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := z.Stop(ctx); err != nil {
		t.Fatal("Stop must be idempotent")
	}
}

func TestApplyConfig_LevelOnlyKeepsInstance(t *testing.T) {
	z, path := newForTest(t, nil)
	before := z.kit.Current()
	if z.kit.Check(zapcore.DebugLevel, "dbg") != nil {
		t.Fatal("debug must be disabled at info level")
	}

	// 仅级别变更：实例不换，AtomicLevel 即时生效
	if err := z.ApplyConfig(testCfg(path, "debug")); err != nil {
		t.Fatal(err)
	}
	if z.kit.Current() != before {
		t.Fatal("level-only change must not rebuild the instance")
	}
	if ce := z.kit.Check(zapcore.DebugLevel, "dbg"); ce == nil {
		t.Fatal("debug must be enabled after hot update")
	} else {
		ce.Write(zap.String("via", "check"))
	}
	wantContent(t, path, "dbg")

	// 收敛语义：相同值无操作
	if err := z.ApplyConfig(testCfg(path, "debug")); err != nil {
		t.Fatal(err)
	}
	if z.kit.Current() != before {
		t.Fatal("identical config must be a no-op")
	}
}

func TestApplyConfig_RebuildOnStructuralChange(t *testing.T) {
	z, path := newForTest(t, nil)
	if err := z.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := z.kit.Current()

	// 编码变更：重建实例并同步全局
	next := testCfg(path, "info")
	next.Format = "json"
	if err := z.ApplyConfig(next); err != nil {
		t.Fatal(err)
	}
	if z.kit.Current() == before {
		t.Fatal("structural change must rebuild the instance")
	}
	z.kit.Info("after-rebuild", zap.String("k", "v"))
	wantContent(t, path, `"msg":"after-rebuild"`)
	zap.L().Info("global-after-rebuild")
	wantContent(t, path, "global-after-rebuild")

	// 轮转参数与控制台开关同属重建面：变更触发重建
	next.MaxSize = 64
	next.LogToConsole = true
	if err := z.ApplyConfig(next); err != nil {
		t.Fatal(err)
	}
	if z.kit.Current() == before {
		t.Fatal("rotation/console change must rebuild the instance")
	}
}

func TestApplyConfig_RejectInvalidKeepsOld(t *testing.T) {
	z, _ := newForTest(t, nil)
	before := z.kit.Current()
	if err := z.ApplyConfig(testCfg(filepath.Join(t.TempDir(), "x.log"), "loud")); err == nil {
		t.Fatal("invalid level must be rejected")
	}
	if z.kit.Current() != before {
		t.Fatal("rejected config must leave state untouched")
	}
	if z.kit.Check(zapcore.DebugLevel, "dbg") != nil {
		t.Fatal("level must stay at info")
	}
}

func TestError_StackStartsAtCaller(t *testing.T) {
	z, path := newForTest(t, nil)
	z.kit.Error("boom", zap.Int("code", 7))

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	i := strings.Index(out, `"stack"`)
	if i < 0 {
		t.Fatalf("no stack field: %s", out)
	}
	q := strings.Index(out[i:], `": "`)
	if q < 0 {
		t.Fatalf("stack field malformed: %s", out[i:])
	}
	rest := out[i+q+3:]
	// 栈首帧（第一个 file:line 段）必须是调用方代码行，而非 kit 内部封装帧
	iCaller, iKit := strings.Index(rest, "zap_test.go"), strings.Index(rest, "kit.go")
	if iCaller < 0 {
		t.Fatalf("caller frame missing from stack: %s", rest)
	}
	if iKit >= 0 && iKit < iCaller {
		t.Fatalf("stack must start at caller, kit frame leaked on top: %s", rest)
	}
}

func TestNewLogger_StandaloneKit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kit.log")

	kit, err := NewLogger(testCfg(path, "info"))
	if err != nil {
		t.Fatal(err)
	}
	defer kit.Close()
	kit.Current().Info("kit-line")
	if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), "kit-line") {
		t.Fatalf("kit logger not writing to file: %q (%v)", string(b), err)
	}

	// Apply 择路：仅级别变更实例不换
	before := kit.Current()
	if err := kit.Apply(testCfg(path, "warn")); err != nil {
		t.Fatal(err)
	}
	if kit.Current() != before {
		t.Fatal("level-only Apply must not rebuild")
	}
	// 重建路径换实例并同步级别
	next := testCfg(path, "warn")
	next.Format = "json"
	if err := kit.Rebuild(next); err != nil {
		t.Fatal(err)
	}
	if kit.Current() == before {
		t.Fatal("rebuild must replace the instance")
	}
	if kit.Current().Check(zapcore.InfoLevel, "x") != nil || kit.Current().Check(zapcore.WarnLevel, "x") == nil {
		t.Fatal("rebuild must apply the new level")
	}
}

func TestWithWatch_AutoApply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch.log")

	var apply func(Config) error
	cancelled := false
	kit, err := NewLogger(testCfg(path, "info"),
		WithWatch(func(a func(Config) error) func() {
			apply = a
			return func() { cancelled = true }
		}))
	if err != nil {
		t.Fatal(err)
	}
	defer kit.Close()
	if apply == nil {
		t.Fatal("watcher must be invoked to subscribe")
	}

	before := kit.Current()
	// 首调收敛：相同值无操作
	if err := apply(testCfg(path, "info")); err != nil {
		t.Fatal(err)
	}
	if kit.Current() != before {
		t.Fatal("identical config must be a no-op")
	}
	// 节变更经 apply 自动生效：仅级别变更实例不换
	if err := apply(testCfg(path, "debug")); err != nil {
		t.Fatal(err)
	}
	if kit.Current() != before {
		t.Fatal("level-only change must not rebuild")
	}
	if kit.Current().Check(zapcore.DebugLevel, "x") == nil {
		t.Fatal("level must be applied")
	}
	// 其余字段变更：kit 内部换实例
	next := testCfg(path, "info")
	next.Format = "json"
	if err := apply(next); err != nil {
		t.Fatal(err)
	}
	if kit.Current() == before {
		t.Fatal("structural change must rebuild")
	}
	// Close 取消订阅
	kit.Close()
	if !cancelled {
		t.Fatal("Close must cancel the watch")
	}
}

// recCore 记录型旁路 core（WithCore 测试用）。
type recCore struct {
	mu   sync.Mutex
	msgs []string
}

func (c *recCore) Enabled(zapcore.Level) bool { return true }
func (c *recCore) Sync() error                { return nil }
func (c *recCore) Check(e zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	return ce.AddCore(e, c)
}
func (c *recCore) Write(e zapcore.Entry, _ []zapcore.Field) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, e.Message)
	return nil
}
func (c *recCore) With([]zapcore.Field) zapcore.Core { return c }

func (c *recCore) messages() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.msgs...)
}

// 旁路 core 与自建 core 并联输出，且热更重建自动带上（构建参数而非
// 一次性注入）。
func TestWithCore_TeeAndRebuildSurvives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	cfg := testCfg(path, "info")
	rec := &recCore{}
	kit, err := NewLogger(cfg, WithCore(rec))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(kit.Close)

	kit.Info("before_rebuild")
	wantContent(t, path, "before_rebuild") // 自建 core 正常落盘
	if got := rec.messages(); len(got) != 1 || got[0] != "before_rebuild" {
		t.Fatalf("extra core records = %v, want [before_rebuild]", got)
	}

	rebuilt := testCfg(path, "info")
	rebuilt.Format = "json"
	if err := kit.Apply(rebuilt); err != nil {
		t.Fatal(err)
	}
	kit.Info("after_rebuild")
	if got := rec.messages(); len(got) != 2 || got[1] != "after_rebuild" {
		t.Fatalf("extra core must survive rebuild, records = %v", got)
	}
}

// WithCore(nil) 忽略（logs_enabled 缺省的 otelc.LogCore() 即 nil）。
func TestWithCore_NilIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if _, err := NewLogger(testCfg(path, "info"), WithCore(nil)); err != nil {
		t.Fatalf("WithCore(nil) must be ignored: %v", err)
	}
}

// 组件壳 New 的 opts 透传（zapc.New(cfg, WithCore(...)) 组合形态）。
func TestNew_PassesOptionsThrough(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	rec := &recCore{}
	z, err := New(testCfg(path, "info"), WithCore(rec))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = z.Stop(context.Background()) })
	z.kit.Info("via_new")
	if got := rec.messages(); len(got) != 1 || got[0] != "via_new" {
		t.Fatalf("records = %v, want [via_new]", got)
	}
}

// rebindDec 是实现 Rebind 协议的装饰桩：只记录重绑次数（zapc 接管的
// 装饰器路径验证用）。
type rebindDec struct {
	mu   sync.Mutex
	hits int
}

func (d *rebindDec) Enabled(context.Context, slog.Level) bool              { return false }
func (d *rebindDec) Log(context.Context, slog.Level, string, ...slog.Attr) {}
func (d *rebindDec) Rebind(observ.Logger) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.hits++
}

func (d *rebindDec) rebinds() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.hits
}

// 接管尊重 Rebind 协议：默认日志器是装饰器时不整体替换，而是原地重绑
// （初始接管与每次热更重建各一次）——otelc 链路注入由此在 zapc 接管与
// 热更后存活。
func TestNew_TakeoverRebindsDecorator(t *testing.T) {
	old := observ.SetDefaultLogger(observ.NoopLogger)
	defer observ.SetDefaultLogger(old)
	dec := &rebindDec{}
	observ.SetDefaultLogger(dec)

	path := filepath.Join(t.TempDir(), "takeover.log")
	cfg := testCfg(path, "info")
	z, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = z.Stop(context.Background()) })

	if observ.DefaultLogger() != observ.Logger(dec) {
		t.Fatal("takeover must keep the Rebind-capable default in place")
	}
	next := cfg
	next.Format = "json"
	if err := z.ApplyConfig(next); err != nil {
		t.Fatal(err)
	}
	if got := dec.rebinds(); got != 2 {
		t.Fatalf("Rebind calls = %d, want 2 (takeover + rebuild)", got)
	}
}
