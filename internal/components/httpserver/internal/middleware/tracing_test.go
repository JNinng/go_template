package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// installTracer 装配带 span 记录器的全局 TracerProvider 与 W3C 传播器
// （otel ≥1.33 全局传播器缺省 noop，须显式装），测试结束恢复。
func installTracer(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
	return sr
}

// remoteSpanContext 构造外部 span 上下文（remote + sampled）与对应
// traceparent 头值。
func remoteSpanContext() (oteltrace.SpanContext, string) {
	tid := oteltrace.TraceID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}
	sid := oteltrace.SpanID{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18}
	sc := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		Remote:     true,
		TraceFlags: oteltrace.FlagsSampled,
	})
	return sc, "00-" + tid.String() + "-" + sid.String() + "-01"
}

func newTracedRequest(remoteAddr, path string, hdr map[string]string) (*http.Request, *httptest.ResponseRecorder) {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.RemoteAddr = remoteAddr
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r, httptest.NewRecorder()
}

func spanAttr(sp sdktrace.ReadOnlySpan, key string) (string, bool) {
	for _, kv := range sp.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.AsString(), true
		}
	}
	return "", false
}

func newTracingChain(t *testing.T) http.Handler {
	t.Helper()
	return newTestChain(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/api/echo", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})
}

// 可信来源（RemoteAddr ∈ trusted_proxies，默认含 loopback）：继承请求头
// traceparent 为父 span；RequestID 兜底为 span 的 TraceID；span 名回填
// 路由模板 + http.route 属性。
func TestTracing_TrustedInheritsParent(t *testing.T) {
	sr := installTracer(t)
	h := newTracingChain(t)
	sc, tpHeader := remoteSpanContext()

	r, w := newTracedRequest("127.0.0.1:5555", "/api/echo", map[string]string{"traceparent": tpHeader})
	h.ServeHTTP(w, r)

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1: %v", len(spans), spans)
	}
	sp := spans[0]
	if got := sp.Parent().SpanID(); got != sc.SpanID() {
		t.Fatalf("parent = %s, want inherited %s（可信来源须串联外部链路）", got, sc.SpanID())
	}
	if len(sp.Links()) != 0 {
		t.Fatalf("trusted request must not produce links, got %v", sp.Links())
	}
	if sp.Name() != "GET /api/echo" {
		t.Errorf("span name = %q, want \"GET /api/echo\"（匹配后回填路由模板）", sp.Name())
	}
	if attr, ok := spanAttr(sp, "http.route"); !ok || attr != "/api/echo" {
		t.Errorf("http.route = %v (found=%v), want /api/echo", attr, ok)
	}
	if got := w.Header().Get("X-Request-ID"); got != sp.SpanContext().TraceID().String() {
		t.Errorf("X-Request-ID = %s, want span TraceID %s（RequestID 兜底与链路同源）", got, sp.SpanContext().TraceID().String())
	}
	if got := w.Header().Get("X-Trace-ID"); got != sp.SpanContext().TraceID().String() {
		t.Errorf("X-Trace-ID = %s, want span TraceID %s（继承链路原样回显）", got, sp.SpanContext().TraceID().String())
	}
}

// 不可信来源：忽略外部 traceparent 的父语义（新建 root span），但转成
// Link 保留关联——外部链路无法污染内部拓扑，排查仍可回溯。
func TestTracing_UntrustedRootWithLink(t *testing.T) {
	sr := installTracer(t)
	h := newTracingChain(t)
	sc, tpHeader := remoteSpanContext()

	r, w := newTracedRequest("203.0.113.50:9999", "/api/echo", map[string]string{"traceparent": tpHeader})
	h.ServeHTTP(w, r)

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	sp := spans[0]
	if sp.Parent().IsValid() {
		t.Fatalf("untrusted request must start a root span, got parent %s", sp.Parent().SpanID())
	}
	if len(sp.Links()) != 1 || sp.Links()[0].SpanContext.SpanID() != sc.SpanID() {
		t.Fatalf("links = %v, want single link to external span %s（外部 traceparent 转 Link）", sp.Links(), sc.SpanID())
	}
	// 伪造的 traceparent 不得成为 RequestID 兜底来源——兜底取新 root span 的 TraceID
	if got := w.Header().Get("X-Request-ID"); got != sp.SpanContext().TraceID().String() {
		t.Errorf("X-Request-ID = %s, want new root span TraceID %s", got, sp.SpanContext().TraceID().String())
	}
	// X-Trace-ID 回显新 root 的 TraceID，外部伪造值不回流
	if got := w.Header().Get("X-Trace-ID"); got != sp.SpanContext().TraceID().String() || got == sc.TraceID().String() {
		t.Errorf("X-Trace-ID = %s, want new root TraceID %s（外部伪造的 %s 不得回显）",
			got, sp.SpanContext().TraceID().String(), sc.TraceID().String())
	}
}

// 未匹配路由的 span 名保持 method（无模板可回填）。
func TestTracing_UnmatchedKeepsMethodName(t *testing.T) {
	sr := installTracer(t)
	h := newTracingChain(t)
	r, w := newTracedRequest("203.0.113.50:9999", "/nope", nil)
	h.ServeHTTP(w, r)

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("spans = %d, want 1", len(spans))
	}
	if name := spans[0].Name(); name != http.MethodGet {
		t.Fatalf("unmatched span name = %q, want %q", name, http.MethodGet)
	}
}
