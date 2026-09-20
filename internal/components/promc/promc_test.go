package promc

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.Addr != ":9090" || c.MetricsPath != "/metrics" || c.HealthPath != "/health" {
		t.Fatalf("Default = %+v", c)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"ok", Default(), false},
		{"empty addr", Config{Addr: "", MetricsPath: "/m", HealthPath: "/h"}, true},
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

// 全链路：Start 后 /metrics 含 Go 运行时指标、/health 恒 healthy（零登记），
// Stop 后端口关闭，二次 Stop 幂等。
func TestStartStop_Endpoints(t *testing.T) {
	cfg := Default()
	cfg.Addr = "127.0.0.1:0"
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
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
	body := make([]byte, 1<<16)
	n, _ := resp.Body.Read(body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics status = %d", resp.StatusCode)
	}
	if !strings.Contains(string(body[:n]), "go_goroutines") {
		t.Errorf("/metrics body missing Go runtime metrics:\n%s", body[:n])
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
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); err == nil {
		_ = p.Stop(context.Background())
		t.Fatal("Start on occupied port must fail")
	}
}

// 健康聚合：登记通过 / 失败检查各一 → 503 + details；全通过 → 200；
// 非 GET → 405。
func TestHealthAggregation(t *testing.T) {
	p, err := New(Default())
	if err != nil {
		t.Fatal(err)
	}
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

	p2, err := New(Default())
	if err != nil {
		t.Fatal(err)
	}
	w2 := httptest.NewRecorder()
	p2.HealthHandler().ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/health", nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("zero checks status = %d, want 200", w2.Code)
	}

	if w3 := do(http.MethodPost); w3.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", w3.Code)
	}
}

// Meter 闭环：observ.Meter 注册的指标出现在 exposition 输出中。
func TestMeterExposition(t *testing.T) {
	p, err := New(Default())
	if err != nil {
		t.Fatal(err)
	}
	p.Meter().NewCounter("promc_test_events_total", "h").Add(3)
	p.Meter().NewGauge("promc_test_depth", "h").Set(7)
	p.Meter().NewHistogram("promc_test_latency_seconds", "h", []float64{0.1, 1}).Observe(0.5)

	srv := httptest.NewServer(p.MetricsHandler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := make([]byte, 1<<16)
	n, _ := resp.Body.Read(body)
	s := string(body[:n])
	for _, want := range []string{"promc_test_events_total 3", "promc_test_depth 7", "promc_test_latency_seconds"} {
		if !strings.Contains(s, want) {
			t.Errorf("exposition missing %q", want)
		}
	}
}
