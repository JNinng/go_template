package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"go_template/internal/components/promc"
	"go_template/pkg/version"
)

// newTestServer 起一个 127.0.0.1:0 的真实 server（离线可跑），返回组件
// 与 base URL；Stop 挂 t.Cleanup。register 在 Start 前调用（业务路由
// 注册窗口）。
func newTestServer(t *testing.T, mutate func(*Config), opts ...Option) (*Server, string) {
	return newTestServerWithRoutes(t, mutate, nil, opts...)
}

func newTestServerWithRoutes(t *testing.T, mutate func(*Config), register func(*Server), opts ...Option) (*Server, string) {
	t.Helper()
	cfg := Default()
	if mutate != nil {
		mutate(&cfg)
	}
	cfg.Addr = "127.0.0.1:0"
	s, err := New(cfg, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if register != nil {
		register(s)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	return s, "http://" + s.Addr()
}

func httpGet(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body %s: %v", url, err)
	}
	return resp, string(body)
}

// 全链路端点与响应头：探活/就绪/版本/pprof 可用，所有响应带
// X-Instance-IDs 与 X-Request-ID；Stop 关端口且幂等。
func TestStartStop_Endpoints(t *testing.T) {
	s, base := newTestServer(t, nil, WithService("demo", "dev", "v1.0.0"))

	resp, body := httpGet(t, base+"/livez")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("livez = %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Instance-IDs") != s.instanceIDs {
		t.Fatalf("X-Instance-IDs = %q, want %q", resp.Header.Get("X-Instance-IDs"), s.instanceIDs)
	}
	if id := resp.Header.Get("X-Request-ID"); len(id) != 32 { // 无 provider 时 crypto/rand 32 hex
		t.Fatalf("X-Request-ID = %q, want 32-hex fallback", id)
	}

	if resp, _ := httpGet(t, base+"/readyz"); resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz = %d, want 200", resp.StatusCode)
	}

	resp, body = httpGet(t, base+"/version")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("version status = %d", resp.StatusCode)
	}
	var v map[string]string
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"version", "commit", "date", "build_time", "go_version"} {
		if _, ok := v[key]; !ok {
			t.Errorf("version body missing %q: %s", key, body)
		}
	}
	if v["version"] != version.Version {
		t.Errorf("version = %q, want %q", v["version"], version.Version)
	}

	_, body = httpGet(t, base+"/debug/pprof/")
	if !strings.Contains(body, "Types of profiles") {
		t.Fatalf("pprof index missing profile list:\n%s", body)
	}

	// 探针仅 GET：405 带 Allow
	req, _ := http.NewRequest(http.MethodPost, base+"/livez", nil)
	postResp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = postResp.Body.Close()
	if postResp.StatusCode != http.StatusMethodNotAllowed || postResp.Header.Get("Allow") != "GET" {
		t.Fatalf("POST livez = %d Allow=%q, want 405 GET", postResp.StatusCode, postResp.Header.Get("Allow"))
	}

	// 未匹配路由 404（进访问日志与指标，详见 middleware 测试）
	if resp, _ := httpGet(t, base+"/nope"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown path = %d, want 404", resp.StatusCode)
	}

	// Stop：端口关闭、幂等
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := (&http.Client{Timeout: 2 * time.Second}).Get(base + "/livez"); err == nil {
		t.Fatal("port must be closed after Stop")
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop must be idempotent: %v", err)
	}
}

// 端口占用在启动期暴露（fail-fast）。
func TestStart_PortInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	cfg := Default()
	cfg.Addr = ln.Addr().String()
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err == nil {
		_ = s.Stop(context.Background())
		t.Fatal("Start on occupied port must fail")
	}
}

// 路由注册窗口：Start 后 Handle panic（装配期错误尽早暴露）。
func TestHandleAfterStartPanics(t *testing.T) {
	s, _ := newTestServer(t, nil)
	defer func() {
		if recover() == nil {
			t.Fatal("Handle after Start must panic")
		}
	}()
	s.Handle("/late", http.NotFoundHandler())
}

// 业务路由 + RequestID 助手：合法请求头原样回写；ctx 注入与响应头一致。
func TestBusinessRoute_RequestIDHelper(t *testing.T) {
	_, base := newTestServerWithRoutes(t, nil, func(s *Server) {
		s.HandleFunc("/api/echo", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, RequestID(r.Context()))
		})
	})

	req, _ := http.NewRequest(http.MethodGet, base+"/api/echo", nil)
	req.Header.Set("X-Request-ID", "client-id-42")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "client-id-42" {
		t.Fatalf("RequestID(ctx) = %q, want client-id-42（业务侧与提取一致）", body)
	}
	if got := resp.Header.Get("X-Request-ID"); got != "client-id-42" {
		t.Fatalf("response X-Request-ID = %q, want client-id-42（原样回写）", got)
	}
}

// 排空摘流：drain 置位后 readiness 503（draining）、liveness 仍 200。
func TestDrain_ReadinessFlips(t *testing.T) {
	s, base := newTestServer(t, nil)

	s.drain.Store(true)
	resp, body := httpGet(t, base+"/readyz")
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "draining") {
		t.Fatalf("readyz while draining = %d %s, want 503 draining", resp.StatusCode, body)
	}
	if resp, _ := httpGet(t, base+"/livez"); resp.StatusCode != http.StatusOK {
		t.Fatalf("livez while draining = %d, want 200（摘流由 readiness 承担）", resp.StatusCode)
	}
}

// 排空端到端：在途慢请求在 Stop（drain_aware）后仍完整返回 200。
func TestStop_DrainsInFlight(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})

	cfg := Default()
	cfg.Addr = "127.0.0.1:0"
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.HandleFunc("/api/slow", func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusOK)
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	base := "http://" + s.Addr()

	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(base + "/api/slow")
		if err != nil {
			done <- result{err: err}
			return
		}
		_ = resp.Body.Close()
		done <- result{code: resp.StatusCode}
	}()

	<-entered                      // 请求已进 handler
	go func() { close(release) }() // 放行慢请求
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case r := <-done:
		if r.err != nil || r.code != http.StatusOK {
			t.Fatalf("in-flight request = code %d err %v, want 200 nil（排空期在途请求须完整服务）", r.code, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight request not completed within drain")
	}
}

// WithProm 单端口收编：/metrics 挂业务端口（含 httpserver 指标）、
// /health 聚合、readiness 复用健康检查（任一失败 503）。
func TestWithProm_Mount(t *testing.T) {
	pm, err := promc.New(promc.Default())
	if err != nil {
		t.Fatal(err)
	}
	pm.RegisterCheck("ok_check", func() error { return nil })

	_, base := newTestServerWithRoutes(t, nil, func(s *Server) {
		s.HandleFunc("/api/hit", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	}, WithProm(pm))

	if _, err := (&http.Client{Timeout: 5 * time.Second}).Get(base + "/api/hit"); err != nil {
		t.Fatal(err)
	}

	resp, body := httpGet(t, base+"/metrics")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "httpserver_requests_total") {
		t.Fatalf("metrics = %d, missing httpserver metrics:\n%s", resp.StatusCode, body)
	}

	if resp, _ := httpGet(t, base+"/health"); resp.StatusCode != http.StatusOK {
		t.Fatalf("health = %d, want 200", resp.StatusCode)
	}
	if resp, _ := httpGet(t, base+"/readyz"); resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz = %d, want 200（checks 全过）", resp.StatusCode)
	}

	pm.RegisterCheck("bad_check", func() error { return fmt.Errorf("boom") })
	if resp, _ := httpGet(t, base+"/readyz"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readyz after failing check = %d, want 503", resp.StatusCode)
	}
	if resp, _ := httpGet(t, base+"/livez"); resp.StatusCode != http.StatusOK {
		t.Fatalf("livez = %d, want 200（liveness 不受健康检查影响）", resp.StatusCode)
	}
}

// TestSection_SelfDeclaration 钉住节名契约：组件自述与文档声明的节名
// 恒一致（字面量漂移在此暴露，而非运行期才被装配校验发现）。
func TestSection_SelfDeclaration(t *testing.T) {
	s := &Server{}
	if s.Section() != SectionName || SectionName != "httpserver" {
		t.Fatalf("section self-declaration drifted: method=%q const=%q", s.Section(), SectionName)
	}
}
