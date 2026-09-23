package config

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// sectionCfg 与 bus_test 的 demoCfg 同形（严格解码要求字段面一致）。
type sectionCfg struct {
	Message string `yaml:"message"`
	Count   int    `yaml:"interval_seconds"`
}

func newSectionTree(t *testing.T) (*Tree, string) {
	t.Helper()
	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml",
		"svc:\n  message: initial\n  interval_seconds: 3\n")
	tr, err := Load(base, "")
	if err != nil {
		t.Fatal(err)
	}
	return tr, dir
}

func TestSection_GetMatchesDecode(t *testing.T) {
	tr, _ := newSectionTree(t)
	s := Bind(tr, "svc", sectionCfg{Message: "def", Count: 1})
	if s.Name() != "svc" {
		t.Fatalf("Name = %q, want svc", s.Name())
	}
	got, err := s.Get()
	if err != nil {
		t.Fatal(err)
	}
	want, err := Decode(tr, "svc", sectionCfg{Message: "def", Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Get = %+v, want %+v (Decode 同路径)", got, want)
	}
}

func TestSection_MissingSectionReturnsBase(t *testing.T) {
	tr, _ := newSectionTree(t)
	got, err := Bind(tr, "absent", sectionCfg{Message: "def", Count: 1}).Get()
	if err != nil {
		t.Fatal(err)
	}
	if got != (sectionCfg{Message: "def", Count: 1}) {
		t.Fatalf("missing section must fall back to base, got %+v", got)
	}
}

func TestSection_StrictUnknownKeyErrors(t *testing.T) {
	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml", "svc:\n  nope: 1\n")
	tr, err := Load(base, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Bind(tr, "svc", sectionCfg{}).Get(); err == nil {
		t.Fatal("unknown key must fail decode")
	}
}

func TestSection_PullSeesLatestMergeWithoutSubscription(t *testing.T) {
	tr, _ := newSectionTree(t)
	s := Bind(tr, "svc", sectionCfg{Message: "def", Count: 1})

	// 远程快照推送（合并于本地层之上）：pull 无需订阅即见新值
	var pushed atomic.Bool
	src := &sectionSrc{fn: func(ctx context.Context, push func(map[string]any)) error {
		if pushed.CompareAndSwap(false, true) {
			push(map[string]any{"svc": map[string]any{"message": "hot"}})
		}
		<-ctx.Done()
		return nil
	}}
	if err := tr.Attach(src); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := s.Get()
		if err != nil {
			t.Fatal(err)
		}
		// 远程快照缺的键由下层文件供值（分层合并语义）
		if got == (sectionCfg{Message: "hot", Count: 3}) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Get did not observe the remote snapshot")
}

// sectionSrc 是测试内最小 Source（bus_test 的 watchRec 之外的第二形态）。
type sectionSrc struct {
	fn func(ctx context.Context, push func(map[string]any)) error
}

func (s *sectionSrc) Name() string { return "section-test" }

func (s *sectionSrc) Start(ctx context.Context, push func(map[string]any)) error {
	return s.fn(ctx, push)
}
