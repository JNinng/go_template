package otelc

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"go_template/internal/config"

	"github.com/jninng/observ"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/resource"
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
	if c.Endpoint != "" || c.Protocol != "grpc" {
		t.Fatalf("Default = %+v, want empty endpoint + grpc", c)
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

// WithService 资源标识：span 的 resource 携带 service.name 与环境声明。
func TestNew_WithServiceResource(t *testing.T) {
	restoreGlobals(t)
	tr, err := New(Default(), WithService("demo-app", "dev"))
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
	for _, want := range []string{"service.name=demo-app", "deployment.environment.name=dev"} {
		if !strings.Contains(s, want) {
			t.Errorf("resource %q missing %q", s, want)
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
	content := "otelc:\n  endpoint: \"127.0.0.1:14317\"\n  protocol: http\n"
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
	if got.Endpoint != "127.0.0.1:14317" || got.Protocol != "http" {
		t.Fatalf("decoded = %+v", got)
	}
}
