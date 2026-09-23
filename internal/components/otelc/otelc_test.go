package otelc

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"go_template/internal/components/zapc"
	"go_template/internal/config"
	"go_template/pkg/ctxkey"

	"github.com/jninng/observ"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.uber.org/zap"
)

// New 装配三处全局（otel provider、slog 默认、observ 默认），测试须快照
// 恢复，避免污染同包其余测试。
func restoreGlobals(t *testing.T) {
	t.Helper()
	prevTP := otel.GetTracerProvider()
	prevSlog := slog.Default()
	prevObs := observ.SetDefaultLogger(observ.NoopLogger)
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		slog.SetDefault(prevSlog)
		observ.SetDefaultLogger(prevObs)
	})
}

// newLogBackend 把 slog 默认后端切到内存 buffer（text 编码）并同步
// observ 默认为该后端——New 的装饰包裹构造时刻的 observ 默认，须先装
// 好再构造。返回 buffer。
func newLogBackend(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevSlog := slog.Default()
	prevObs := observ.SetDefaultLogger(observ.NoopLogger)
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	observ.SetDefaultLogger(observ.NewSlogLogger(slog.Default()))
	t.Cleanup(func() {
		slog.SetDefault(prevSlog)
		observ.SetDefaultLogger(prevObs)
	})
	return &buf
}

func TestDefault(t *testing.T) {
	c := Default()
	if c.Endpoint != "" || c.Protocol != "grpc" || c.LogsEnabled {
		t.Fatalf("Default = %+v, want empty endpoint + grpc + logs off", c)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"grpc ok", Config{Protocol: "grpc"}, false},
		{"http ok", Config{Protocol: "http"}, false},
		{"empty protocol rejected", Config{Protocol: ""}, true},
		{"bad protocol", Config{Protocol: "thrift"}, true},
		{"logs without endpoint rejected", Config{Protocol: "grpc", LogsEnabled: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate(%+v) err = %v, wantErr %v", tc.cfg, err, tc.wantErr)
			}
		})
	}
}

func TestNew_InvalidConfigRejected(t *testing.T) {
	restoreGlobals(t)
	if _, err := New(Config{Endpoint: "x:1", Protocol: "bad"}); err == nil {
		t.Fatal("invalid protocol must fail construction")
	}
}

// 空 endpoint 承重行为：provider 照常安装，trace_id 生成能力可用。
func TestNew_EmptyEndpointInstallsProvider(t *testing.T) {
	restoreGlobals(t)
	tr, err := New(Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, span := otel.Tracer("otelc-test").Start(context.Background(), "op")
	if !span.SpanContext().IsValid() {
		t.Fatal("span context must be valid without exporter")
	}
	span.End()
	if err := tr.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// 日志 trace 注入：ctx 携带有效 span 时自动附加 trace_id/span_id；
// 无 span 时原样透传（零属性）。
func TestNew_LogTraceInjected(t *testing.T) {
	restoreGlobals(t)
	buf := newLogBackend(t)
	tr, err := New(Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Stop(context.Background()) })

	observ.DefaultLogger().Log(context.Background(), slog.LevelInfo, "no_span")

	ctx, span := otel.Tracer("otelc-test").Start(context.Background(), "op")
	defer span.End()
	observ.DefaultLogger().Log(ctx, slog.LevelInfo, "in_span")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2\n%s", len(lines), buf.String())
	}
	if strings.Contains(lines[0], "trace_id=") {
		t.Errorf("no_span line must not carry trace attrs: %q", lines[0])
	}
	traceRe := regexp.MustCompile(`trace_id=[0-9a-f]{32}`)
	spanRe := regexp.MustCompile(`span_id=[0-9a-f]{16}`)
	if !traceRe.MatchString(lines[1]) || !spanRe.MatchString(lines[1]) {
		t.Errorf("in_span line missing trace attrs: %q", lines[1])
	}
	if want := span.SpanContext().TraceID().String(); !strings.Contains(lines[1], want) {
		t.Errorf("in_span trace_id mismatch, want %s: %q", want, lines[1])
	}
}

// WithService 资源标识：span 的 resource 携带 service.name、环境声明与版本。
func TestNew_WithServiceResource(t *testing.T) {
	restoreGlobals(t)
	tr, err := New(Default(), WithService("demo-app", "dev", "v1.2.3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Stop(context.Background()) })

	_, span := otel.Tracer("otelc-test").Start(context.Background(), "op")
	defer span.End()
	ro, ok := span.(interface{ Resource() *resource.Resource })
	if !ok {
		t.Fatal("sdk span must expose Resource")
	}
	s := ro.Resource().String()
	for _, want := range []string{"service.name=demo-app", "deployment.environment.name=dev", "service.version=v1.2.3"} {
		if !strings.Contains(s, want) {
			t.Errorf("resource %q missing %q", s, want)
		}
	}
}

// WithInstanceID 资源标识：service.instance.id 进 resource。
func TestNew_WithInstanceIDResource(t *testing.T) {
	restoreGlobals(t)
	tr, err := New(Default(), WithService("demo-app", "dev", "v1.2.3"), WithInstanceID("demo-app:pod-1"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Stop(context.Background()) })

	_, span := otel.Tracer("otelc-test").Start(context.Background(), "op")
	defer span.End()
	ro := span.(interface{ Resource() *resource.Resource })
	if s := ro.Resource().String(); !strings.Contains(s, "service.instance.id=demo-app:pod-1") {
		t.Errorf("resource %q missing service.instance.id", s)
	}
}

// request_id 注入：ctx 携带 request_id（pkg/ctxkey）时日志自动附加，
// 与 trace_id/span_id 并存；未携带时零属性差异。
func TestNew_RequestIDInjected(t *testing.T) {
	restoreGlobals(t)
	buf := newLogBackend(t)
	tr, err := New(Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Stop(context.Background()) })

	observ.DefaultLogger().Log(context.Background(), slog.LevelInfo, "no_id")

	ctx, span := otel.Tracer("otelc-test").Start(context.Background(), "op")
	defer span.End()
	observ.DefaultLogger().Log(ctxkey.WithRequestID(ctx, "req-42"), slog.LevelInfo, "with_id")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2\n%s", len(lines), buf.String())
	}
	if strings.Contains(lines[0], "request_id=") {
		t.Errorf("no_id line must not carry request_id: %q", lines[0])
	}
	for _, want := range []string{"request_id=req-42", "trace_id=", "span_id="} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("with_id line missing %q: %q", want, lines[1])
		}
	}
}

// 不可达 endpoint：构造不失败（gRPC 惰性连接），Stop 在预算内返回且幂等。
func TestNew_UnreachableEndpointNoPanic(t *testing.T) {
	restoreGlobals(t)
	tr, err := New(Config{Endpoint: "127.0.0.1:1", Protocol: "grpc"})
	if err != nil {
		t.Fatalf("lazy gRPC connection must not fail construction: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tr.Stop(ctx); err != nil {
		t.Fatalf("Stop with unreachable exporter: %v", err)
	}
	if err := tr.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop must be idempotent: %v", err)
	}
}

// 配置节解码冒烟：Default 基座 + yaml 覆盖字段（缺失字段回落默认值）。
func TestConfigDecode(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/config.yaml"
	content := "otelc:\n  endpoint: \"127.0.0.1:14317\"\n  protocol: http\n  logs_enabled: true\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	tree, err := config.Load(p, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := config.Decode(tree, "otelc", Default())
	if err != nil {
		t.Fatal(err)
	}
	if got.Endpoint != "127.0.0.1:14317" || got.Protocol != "http" || !got.LogsEnabled {
		t.Fatalf("decoded = %+v", got)
	}
}

// 日志导出信号：缺省关闭（LogCore 为 nil）；启用需 endpoint（无"本地
// 生成"退化语义）；不可达 endpoint 下 core 可写、Stop 预算内返回。
func TestNew_LogCoreDisabledByDefault(t *testing.T) {
	restoreGlobals(t)
	tr, err := New(Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Stop(context.Background()) })
	if tr.LogCore() != nil {
		t.Fatal("LogCore must be nil when logs disabled")
	}
}

func TestNew_LogsWithoutEndpointRejected(t *testing.T) {
	restoreGlobals(t)
	cfg := Default()
	cfg.LogsEnabled = true
	if _, err := New(cfg); err == nil {
		t.Fatal("logs_enabled without endpoint must fail construction")
	}
}

// 链路属性提取器：有效 span 出 trace_id/span_id、ctxkey 出 request_id、
// 皆无时为零属性。zapc.WithCtxAttrs 与 traceLogger（slog 兜底）共用。
func TestCtxLogAttrs(t *testing.T) {
	restoreGlobals(t)
	tr, err := New(Default()) // 安装真实 TracerProvider（否则全局 span 无效）
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Stop(context.Background()) })

	if got := CtxLogAttrs(context.Background()); got != nil {
		t.Fatalf("bare ctx must yield nil, got %v", got)
	}

	ctx, span := otel.Tracer("otelc-test").Start(context.Background(), "op")
	defer span.End()
	byKey := map[string]string{}
	for _, a := range CtxLogAttrs(ctx) {
		byKey[a.Key] = a.Value.String()
	}
	if byKey["trace_id"] != span.SpanContext().TraceID().String() ||
		byKey["span_id"] != span.SpanContext().SpanID().String() {
		t.Fatalf("trace attrs mismatch: %v", byKey)
	}
	if _, ok := byKey["request_id"]; ok {
		t.Fatalf("request_id must be absent without ctxkey: %v", byKey)
	}

	byKey = map[string]string{}
	for _, a := range CtxLogAttrs(ctxkey.WithRequestID(ctx, "req-1")) {
		byKey[a.Key] = a.Value.String()
	}
	if byKey["request_id"] != "req-1" || byKey["trace_id"] == "" {
		t.Fatalf("request_id + trace attrs expected: %v", byKey)
	}
}

// slog 缺省后端的兜底注入：重复安装不叠装饰，链路日志恒带 trace 属性
// （接了 zapc 时接管整体替换本装饰，注入由适配层经 WithCtxAttrs 完成）。
func TestLogTrace_SlogFallbackDecoration(t *testing.T) {
	restoreGlobals(t)
	buf1 := newLogBackend(t)
	tr, err := New(Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Stop(context.Background()) })

	installLogTrace() // 幂等：默认已是装饰，不得叠加

	ctx, span := otel.Tracer("otelc-test").Start(context.Background(), "op")
	defer span.End()
	observ.DefaultLogger().Log(ctx, slog.LevelInfo, "decorated")
	wantTrace := span.SpanContext().TraceID().String()
	if got := strings.Count(buf1.String(), wantTrace); got != 1 {
		t.Fatalf("single decoration expected, trace_id hits = %d:\n%s", got, buf1.String())
	}
}

// 组合回归：otelc 先接线、zapc 后接管（logs_enabled 配方的接线顺序）——
// 接管整体替换装饰，链路注入由 WithCtxAttrs 在 zaplog 适配层完成，
// 热更重建后保持有效（WithCtxAttrs 传参即模板 biz 接线）。
func TestLogTrace_SurvivesZapcTakeoverAndRebuild(t *testing.T) {
	restoreGlobals(t)
	newLogBackend(t)

	tr, err := New(Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Stop(context.Background()) })

	cfg := zapc.Default()
	cfg.Path = filepath.Join(t.TempDir(), "zapc.log")
	cfg.LogToConsole = false
	z, err := zapc.New(cfg, zapc.WithCtxAttrs(CtxLogAttrs))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = z.Stop(context.Background()) })

	logInSpan := func(msg string) {
		ctx, span := otel.Tracer("otelc-test").Start(context.Background(), "op")
		defer span.End()
		observ.DefaultLogger().Log(ctx, slog.LevelInfo, msg)
	}
	logInSpan("after_takeover")

	rebuilt := cfg
	rebuilt.Format = "json"
	if err := z.ApplyConfig(rebuilt); err != nil {
		t.Fatal(err)
	}
	logInSpan("after_rebuild")

	b, err := os.ReadFile(cfg.Path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"after_takeover", "after_rebuild"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in zapc output:\n%s", want, s)
		}
	}
	if n := strings.Count(s, "trace_id"); n != 2 {
		t.Fatalf("both lines must carry trace attrs, trace_id hits = %d:\n%s", n, s)
	}
}

func TestNew_LogsUnreachableEndpointNoPanic(t *testing.T) {
	restoreGlobals(t)
	cfg := Default()
	cfg.Endpoint = "127.0.0.1:1"
	cfg.LogsEnabled = true
	tr, err := New(cfg)
	if err != nil {
		t.Fatalf("lazy gRPC connection must not fail construction: %v", err)
	}
	core := tr.LogCore()
	if core == nil {
		t.Fatal("LogCore must be non-nil when logs enabled")
	}
	zap.New(core).Info("ship_me", zap.String("k", "v")) // 经 core 写入，不得 panic

	// 导出器对拒连端点的 shutdown 重试会吃满预算，收紧到 2s 控制用例时长
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := tr.Stop(ctx); err != nil {
		t.Fatalf("Stop with unreachable log exporter: %v", err)
	}
	if err := tr.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop must be idempotent: %v", err)
	}
}

// TestSection_SelfDeclaration 钉住节名契约：组件自述与文档声明的节名
// 恒一致（字面量漂移在此暴露，而非运行期才被装配校验发现）。
func TestSection_SelfDeclaration(t *testing.T) {
	tr := &Tracer{}
	if tr.Section() != SectionName || SectionName != "otelc" {
		t.Fatalf("section self-declaration drifted: method=%q const=%q", tr.Section(), SectionName)
	}
}
