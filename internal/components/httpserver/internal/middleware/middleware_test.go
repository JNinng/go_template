package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/cors"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"go_template/internal/components/httpserver/internal/metric"
	"go_template/internal/components/httpserver/internal/trust"
)

// defaultTable 与组件配置的缺省网段一致（middleware 不依赖根包 Config，
// 测试就地复制一份）。
func defaultTable(t *testing.T) trust.Table {
	t.Helper()
	table, err := trust.NewTable([]string{
		"127.0.0.0/8", "::1/128",
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
	})
	if err != nil {
		t.Fatal(err)
	}
	return table
}

// newTestChain 组装测试链：mux 由 register 填充，Deps 取缺省形态
// （跳过清单含 /livez /readyz /version /metrics /health /debug/pprof）。
func newTestChain(t *testing.T, register func(*http.ServeMux)) http.Handler {
	t.Helper()
	table := defaultTable(t)
	skip := func(path string) bool {
		switch path {
		case "/livez", "/readyz", "/version", "/metrics", "/health":
			return true
		}
		return strings.HasPrefix(path, "/debug/pprof/")
	}
	mux := http.NewServeMux()
	if register != nil {
		register(mux)
	}
	reg := metric.New(prometheus.NewRegistry())
	return Chain(Deps{
		Proxies:     func() trust.Table { return table },
		InstanceIDs: "test-svc:test-host",
		Metrics:     reg,
		Skip:        skip,
		BodyLimit:   func() int64 { return 10 << 20 },
		CORS: func() *cors.Cors {
			return cors.New(cors.Options{
				AllowedOrigins: []string{"*"},
				AllowedMethods: []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
				AllowedHeaders: []string{"*"},
				MaxAge:         7200,
			})
		},
	}, mux)
}

func doReq(h http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = "203.0.113.50:1000" // 默认走不可信路径（XFF 无视）
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// installZapObserver 把全局 logger 切到内存 observer（访问日志断言用），
// 测试结束恢复。
func installZapObserver(t *testing.T) *observer.ObservedLogs {
	t.Helper()
	core, logs := observer.New(zapcore.InfoLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	t.Cleanup(restore)
	return logs
}

// 访问日志：字段齐全、级别按状态码分档（2xx Info / 4xx Warn / 5xx Error）、
// path 不含 query、Referer 仅在携带时出现。
func TestAccessLog_FieldsAndLevels(t *testing.T) {
	logs := installZapObserver(t)
	h := newTestChain(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/api/ok", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("hello"))
		})
		mux.HandleFunc("/api/fail", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		})
	})

	// 2xx → Info，字段断言
	w := doReq(h, http.MethodGet, "/api/ok?token=secret", map[string]string{"User-Agent": "test-agent/1.0"})
	if w.Code != http.StatusOK {
		t.Fatalf("ok = %d", w.Code)
	}
	entries := logs.TakeAll()
	if len(entries) != 1 || entries[0].Message != "httpserver_request_completed" {
		t.Fatalf("entries = %d, want 1 httpserver_request_completed", len(entries))
	}
	m := entries[0].ContextMap()
	wantFields := map[string]string{
		"method":         http.MethodGet,
		"path":           "/api/ok", // query 不落日志
		"status_code":    "200",
		"client_ip":      "203.0.113.50",
		"user_agent":     "test-agent/1.0",
		"route":          "/api/ok",
		"response_bytes": "5",
		"request_id":     w.Header().Get("X-Request-ID"),
	}
	for k, v := range wantFields {
		if got := fmt.Sprint(m[k]); got != v {
			t.Errorf("field %q = %v, want %v (all: %v)", k, m[k], v, m)
		}
	}
	if entries[0].Level != zapcore.InfoLevel {
		t.Errorf("2xx level = %v, want Info", entries[0].Level)
	}
	if _, ok := m["referer"]; ok {
		t.Error("referer must be omitted when absent")
	}
	if _, ok := m["duration_ms"]; !ok {
		t.Error("log missing duration_ms")
	}
	if _, ok := m["trace_id"]; ok {
		t.Error("trace_id must be omitted without a valid span (本测试无 TracerProvider)")
	}

	// Referer 携带时记录
	doReq(h, http.MethodGet, "/api/ok", map[string]string{"Referer": "https://a.example/x"})
	if m := logs.TakeAll()[0].ContextMap(); fmt.Sprint(m["referer"]) != "https://a.example/x" {
		t.Errorf("referer = %v", m["referer"])
	}

	// 5xx → Error
	doReq(h, http.MethodGet, "/api/fail", nil)
	entries = logs.TakeAll()
	if len(entries) != 1 || entries[0].Level != zapcore.ErrorLevel {
		t.Fatalf("5xx entries = %d level = %v, want 1 Error", len(entries), entries[0].Level)
	}

	// 404（未匹配）→ Warn、无 route
	doReq(h, http.MethodGet, "/nope", nil)
	entries = logs.TakeAll()
	if len(entries) != 1 || entries[0].Level != zapcore.WarnLevel {
		t.Fatalf("404 entries = %d level = %v, want 1 Warn", len(entries), entries[0].Level)
	}
	if m := entries[0].ContextMap(); fmt.Sprint(m["path"]) != "/nope" {
		t.Errorf("404 path = %v", m["path"])
	} else if _, ok := m["route"]; ok {
		t.Error("unmatched request must not carry route")
	}
}

// 跳过清单：探活/就绪/版本/指标/健康/pprof 不产访问日志；实例头照常注入。
func TestSkipList(t *testing.T) {
	logs := installZapObserver(t)
	h := newTestChain(t, func(m *http.ServeMux) {
		m.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {})
		m.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {})
		m.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {})
		m.HandleFunc("/debug/pprof/", func(w http.ResponseWriter, r *http.Request) {})
		m.HandleFunc("/livez", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
		m.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	})

	for _, path := range []string{"/livez", "/readyz", "/version", "/metrics", "/health", "/debug/pprof/"} {
		w := doReq(h, http.MethodGet, path, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200", path, w.Code)
		}
		if got := w.Header().Get("X-Instance-IDs"); got != "test-svc:test-host" {
			t.Errorf("%s X-Instance-IDs = %q（跳过清单路径照常注入）", path, got)
		}
	}
	if n := len(logs.TakeAll()); n != 0 {
		t.Fatalf("skipped paths produced %d log entries, want 0", n)
	}
}

// Recovery：panic → 500 JSON + 专门日志（含 panic_value 与请求字段）；
// panic 请求不产常规访问日志。
func TestRecovery(t *testing.T) {
	logs := installZapObserver(t)
	h := newTestChain(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/api/boom", func(http.ResponseWriter, *http.Request) {
			panic("kaboom")
		})
	})

	w := doReq(h, http.MethodGet, "/api/boom", map[string]string{"X-Request-ID": "req-7"})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("panic status = %d, want 500", w.Code)
	}
	if !strings.Contains(w.Body.String(), "internal server error") {
		t.Fatalf("panic body = %q", w.Body.String())
	}
	entries := logs.TakeAll()
	if len(entries) != 1 || entries[0].Message != "httpserver_panic_recovered" {
		t.Fatalf("entries = %v, want single httpserver_panic_recovered", entries)
	}
	m := entries[0].ContextMap()
	if fmt.Sprint(m["panic_value"]) != "kaboom" || fmt.Sprint(m["request_id"]) != "req-7" {
		t.Errorf("panic log fields = %v", m)
	}
	if entries[0].Level != zapcore.ErrorLevel {
		t.Errorf("panic log level = %v, want Error", entries[0].Level)
	}
	if _, ok := m["stack"]; !ok {
		t.Error("panic log missing stack")
	}
}

// CORS：preflight 短路（不进访问日志）、简单请求带放行头。
func TestCORS(t *testing.T) {
	logs := installZapObserver(t)
	h := newTestChain(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/api/ok", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})

	// preflight：OPTIONS + Origin + ACRM
	w := doReq(h, http.MethodOptions, "/api/ok", map[string]string{
		"Origin":                         "https://a.example",
		"Access-Control-Request-Method":  "POST",
		"Access-Control-Request-Headers": "X-Custom",
	})
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("preflight ACAO = %q, want *", w.Header().Get("Access-Control-Allow-Origin"))
	}
	if n := len(logs.TakeAll()); n != 0 {
		t.Fatalf("preflight produced %d log entries, want 0（CORS 在可观测层外）", n)
	}

	// 简单跨域请求：放行头 + 照常记日志
	w = doReq(h, http.MethodGet, "/api/ok", map[string]string{"Origin": "https://a.example"})
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("ACAO = %q, want *", w.Header().Get("Access-Control-Allow-Origin"))
	}
	if n := len(logs.TakeAll()); n != 1 {
		t.Fatalf("simple request log entries = %d, want 1", n)
	}
}

// RequestID：合法头原样回写；非法头静默忽略走兜底（无 provider 时
// crypto/rand 32 hex）。
func TestRequestID_Validation(t *testing.T) {
	h := newTestChain(t, nil)
	cases := []struct {
		in      string
		wantLen int // 期望的响应头长度
	}{
		{"legal-id_1.2", len("legal-id_1.2")},
		{"", 32},
		{"bad id with spaces", 32},
		{"inject\nattempt", 32},
		{strings.Repeat("x", 65), 32},
	}
	for _, tc := range cases {
		hdr := map[string]string{"X-Request-ID": tc.in}
		if tc.in == "" {
			hdr = nil
		}
		w := doReq(h, http.MethodGet, "/livez", hdr)
		got := w.Header().Get("X-Request-ID")
		if len(got) != tc.wantLen {
			t.Errorf("RequestID(%q) → %q (len %d), want len %d", tc.in, got, len(got), tc.wantLen)
		}
		if traceID := w.Header().Get("X-Trace-ID"); traceID != "" {
			t.Errorf("X-Trace-ID = %q, want empty（本测试无 TracerProvider，无有效 span）", traceID)
		}
	}
}

// 请求体上限：声明超限 413。
func TestBodyLimit(t *testing.T) {
	table := defaultTable(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/echo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := Chain(Deps{
		Proxies:   func() trust.Table { return table },
		Skip:      func(string) bool { return false },
		BodyLimit: func() int64 { return 16 },
		CORS:      func() *cors.Cors { return nil },
	}, mux)

	r := httptest.NewRequest(http.MethodPost, "/api/echo", nil)
	r.RemoteAddr = "127.0.0.1:1"
	r.ContentLength = 17 // 声明即超限
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized declared body = %d, want 413", w.Code)
	}
}
