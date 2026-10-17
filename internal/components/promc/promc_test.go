package promc

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.Addr != "" || c.MetricsPath != "/metrics" || c.HealthPath != "/health" {
		t.Fatalf("Default = %+v", c)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"ok default (no standalone server)", Default(), false},
		{"ok standalone addr", Config{Addr: ":9090", MetricsPath: "/m", HealthPath: "/h"}, false},
		{"empty metrics path", Config{Addr: ":1", MetricsPath: "", HealthPath: "/h"}, true},
		{"path without slash", Config{Addr: ":1", MetricsPath: "metrics", HealthPath: "/h"}, true},
		{"same paths", Config{Addr: ":1", MetricsPath: "/x", HealthPath: "/x"}, true},
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
	if _, err := New(Config{Addr: ":1", MetricsPath: "m", HealthPath: "/h"}); err == nil {
		t.Fatal("invalid config must fail construction")
	}
}

// newProm 构造组件并在测试结束后恢复全局 MeterProvider（New 即安装
// 全局 provider，且 Stop 不回退）。
func newProm(t *testing.T, cfg Config) *Prom {
	t.Helper()
	prev := otel.GetMeterProvider()
	t.Cleanup(func() { otel.SetMeterProvider(prev) })
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// 全链路：Start 后 /metrics 含 Go 运行时指标、/health 恒 healthy（零登记），
// Stop 后端口关闭，二次 Stop 幂等。
func TestStartStop_Endpoints(t *testing.T) {
	cfg := Default()
	cfg.Addr = "127.0.0.1:0"
	p := newProm(t, cfg)
	ctx := context.Background()
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	base := "http://" + p.Addr()
	client := &http.Client{Timeout: 2 * time.Second}

	resp, err := client.Get(base + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read /metrics body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics status = %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "go_goroutines") {
		t.Errorf("/metrics body missing Go runtime metrics:\n%s", body)
	}

	resp, err = client.Get(base + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	var hr struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&hr); err != nil {
		t.Fatalf("decode /health: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || hr.Status != "healthy" {
		t.Fatalf("/health = %d %+v, want 200 healthy", resp.StatusCode, hr)
	}

	if err := p.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := client.Get(base + "/health"); err == nil {
		t.Fatal("port must be closed after Stop")
	}
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("second Stop must be idempotent: %v", err)
	}
}

// 端口占用在启动期暴露（fail-fast），不拖到运行期。
func TestStart_PortInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	cfg := Default()
	cfg.Addr = ln.Addr().String()
	p := newProm(t, cfg)
	if err := p.Start(context.Background()); err == nil {
		_ = p.Stop(context.Background())
		t.Fatal("Start on occupied port must fail")
	}
}

// 单端口形态（缺省）：addr 空 → Start 跳过监听（无副作用）；Stop 跳过
// server 关停但仍回收 provider；MetricsPath / HealthPath 暴露配置路径供
// 挂载方对齐。
func TestStart_EmptyAddrSkipsListener(t *testing.T) {
	p := newProm(t, Default())
	ctx := context.Background()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start with empty addr: %v", err)
	}
	if p.Addr() != "" {
		t.Fatalf("Addr = %q, want empty", p.Addr())
	}
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("Stop with empty addr: %v", err)
	}
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("second Stop must be idempotent: %v", err)
	}
	if p.MetricsPath() != "/metrics" || p.HealthPath() != "/health" {
		t.Fatalf("paths = %q %q", p.MetricsPath(), p.HealthPath())
	}
}

// 健康聚合：登记通过 / 失败检查各一 → 503 + details；全通过 → 200；
// 非 GET → 405。
func TestHealthAggregation(t *testing.T) {
	p := newProm(t, Default())
	p.RegisterCheck("ok_check", func() error { return nil })
	p.RegisterCheck("bad_check", func() error { return context.DeadlineExceeded })

	h := p.HealthHandler()
	do := func(method string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "/health", nil))
		return w
	}

	w := do(http.MethodGet)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	var got CheckResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusUnhealthy {
		t.Errorf("aggregated = %q, want unhealthy", got.Status)
	}
	if got.Details["ok_check"] != "ok" || !strings.Contains(got.Details["bad_check"], "deadline") {
		t.Errorf("details = %+v", got.Details)
	}

	p2 := newProm(t, Default())
	w2 := httptest.NewRecorder()
	p2.HealthHandler().ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/health", nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("zero checks status = %d, want 200", w2.Code)
	}

	if w3 := do(http.MethodPost); w3.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", w3.Code)
	}
}

// New 即安装 otel 全局 MeterProvider；其后经全局创建的仪表路由到本组件
// registry 并出现在 /metrics（闭环证明）。Stop 不回退（进程收尾语义）。
func TestNewInstallsDefaultProvider(t *testing.T) {
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(noop.NewMeterProvider()) // 先复位：证明安装确由 New 完成
	t.Cleanup(func() { otel.SetMeterProvider(prev) })

	p, err := New(Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })

	// 仪表经全局 provider 创建（业务组件的缺省埋点路径），绑定 New 安装
	// 的 prom-backed provider——导出面即本组件的 /metrics。
	m := otel.GetMeterProvider().Meter("promc_test_install")
	c, err := m.Int64Counter("promc_install_probe", otelmetric.WithUnit("1"))
	if err != nil {
		t.Fatal(err)
	}
	c.Add(context.Background(), 1)

	srv := httptest.NewServer(p.MetricsHandler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "promc_install_probe_total 1") {
		t.Errorf("global provider must route to component registry:\n%s", b)
	}
}

// Meter 闭环：otel 仪表（counter / gauge / histogram）注册的指标出现在
// exposition 输出中，命名口径由导出器补后缀（_total / _seconds）。
func TestMeterExposition(t *testing.T) {
	p := newProm(t, Default())
	ctx := context.Background()
	m := p.Meter("promc_test_exposition")

	c, err := m.Int64Counter("promc_test_events", otelmetric.WithDescription("h"), otelmetric.WithUnit("1"))
	if err != nil {
		t.Fatal(err)
	}
	c.Add(ctx, 3)

	g, err := m.Int64Gauge("promc_test_depth", otelmetric.WithDescription("h"))
	if err != nil {
		t.Fatal(err)
	}
	g.Record(ctx, 7)

	h, err := m.Float64Histogram("promc_test_latency",
		otelmetric.WithUnit("s"), otelmetric.WithExplicitBucketBoundaries(0.1, 1))
	if err != nil {
		t.Fatal(err)
	}
	h.Record(ctx, 0.5)

	srv := httptest.NewServer(p.MetricsHandler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"promc_test_events_total 3", "promc_test_depth 7", "promc_test_latency_seconds"} {
		if !strings.Contains(s, want) {
			t.Errorf("exposition missing %q", want)
		}
	}
}

// TestSection_SelfDeclaration 钉住节名契约：组件自述与文档声明的节名
// 恒一致（字面量漂移在此暴露，而非运行期才被装配校验发现）。
func TestSection_SelfDeclaration(t *testing.T) {
	p := &Prom{}
	if p.Section() != SectionName || SectionName != "promc" {
		t.Fatalf("section self-declaration drifted: method=%q const=%q", p.Section(), SectionName)
	}
}
