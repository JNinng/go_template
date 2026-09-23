package httpserver

// 请求日志注入与 ctx 日志通道的行为测试：WithAccessLogger 换源、
// LoggerFrom 富化（zap 全局派生，与注入无关）、非请求语境回落。

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// recLogger 构造写入缓冲区的记录器（无 caller，断言子串不互相干扰）。
func recLogger(buf *strings.Builder) *zap.Logger {
	enc := zapcore.NewConsoleEncoder(zapcore.EncoderConfig{
		TimeKey:      "t",
		LevelKey:     "l",
		MessageKey:   "m",
		EncodeLevel:  zapcore.CapitalLevelEncoder,
		EncodeTime:   zapcore.EpochTimeEncoder,
		EncodeCaller: zapcore.ShortCallerEncoder,
	})
	return zap.New(zapcore.NewCore(enc, zapcore.AddSync(&bufSink{buf}), zapcore.DebugLevel))
}

type bufSink struct{ b *strings.Builder }

func (s *bufSink) Write(p []byte) (int, error) { return s.b.Write(p) }

// WithAccessLogger：访问日志与 panic 日志走注入的记录器（携带 request_id），
// 不落 zap 全局。
func TestWithAccessLogger_RoutesRequestLogs(t *testing.T) {
	var buf strings.Builder
	s0, base := newTestServerWithRoutes(t, nil, func(s *Server) {
		s.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("pong"))
		})
	}, WithAccessLogger(func() *zap.Logger { return recLogger(&buf) }))
	_ = s0

	resp, body := httpGet(t, base+"/api/ping")
	if resp.StatusCode != http.StatusOK || body != "pong" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}

	out := buf.String()
	if !strings.Contains(out, "httpserver_request_completed") {
		t.Fatalf("access log must route to injected recorder:\n%s", out)
	}
	if !strings.Contains(out, `"request_id"`) {
		t.Fatalf("access log must carry request_id:\n%s", out)
	}
}

// LoggerFrom：RequestID 层挂进 ctx 的记录器由 zap 全局派生（业务日志归
// 应用流，req.log 只放访问记录），预绑定 request_id；与 WithAccessLogger
// 注入无关——本测试不注入请求记录器即证明独立性。
func TestLoggerFrom_EnrichesHandlerLogs(t *testing.T) {
	var buf strings.Builder
	restore := zap.ReplaceGlobals(recLogger(&buf))
	t.Cleanup(restore)

	_, base := newTestServerWithRoutes(t, nil, func(s *Server) {
		s.HandleFunc("/api/logged", func(w http.ResponseWriter, r *http.Request) {
			LoggerFrom(r.Context()).Info("from_handler")
			_, _ = w.Write([]byte("ok"))
		})
	})

	if resp, _ := httpGet(t, base+"/api/logged"); resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}

	out := buf.String()
	if n := strings.Count(out, "from_handler"); n != 1 {
		t.Fatalf("handler log hits = %d:\n%s", n, out)
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "from_handler") {
			if !strings.Contains(line, `"request_id"`) {
				t.Fatalf("handler log must carry bound request_id:\n%s", line)
			}
			return
		}
	}
	t.Fatal("handler log line not found")
}

// 非 httpserver 请求语境调 LoggerFrom：回落 zap 全局，不 panic。
func TestLoggerFrom_FallsBackOutsideRequest(t *testing.T) {
	if LoggerFrom(context.Background()) == nil {
		t.Fatal("fallback must be non-nil")
	}
}
