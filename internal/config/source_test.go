package config

import (
	"context"
	"testing"
	"time"
)

// pushSource 把固定行为注入 Source.Start，用于模拟远程配置中心。
type pushSource struct {
	name string                                                     // 源名称
	fn   func(ctx context.Context, push func(map[string]any)) error // Start 行为
}

func (s *pushSource) Name() string { return s.name }
func (s *pushSource) Start(ctx context.Context, push func(map[string]any)) error {
	return s.fn(ctx, push)
}

func TestAttach_WaitsForFirstSnapshot(t *testing.T) {
	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml", "svc:\n  a: 1\n")
	tr, err := Load(base, "")
	if err != nil {
		t.Fatal(err)
	}

	delivered := make(chan struct{})
	src := &pushSource{name: "test", fn: func(ctx context.Context, push func(map[string]any)) error {
		push(map[string]any{"svc": map[string]any{"a": 2, "b": 9}})
		close(delivered)
		<-ctx.Done()
		return nil
	}}
	if err := tr.Attach(src); err != nil {
		t.Fatal(err)
	}
	<-delivered
	// Attach 返回时首份快照必须已合并（同步语义）
	if got := mustRawStr(t, tr, "svc", "a"); got != 2 {
		t.Fatalf("remote should overlay local, got %v", got)
	}
	if got := mustRawStr(t, tr, "svc", "b"); got != 9 {
		t.Fatalf("remote-only key missing, got %v", got)
	}
}

func TestAttach_StartErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml", "svc:\n  a: 1\n")
	tr, err := Load(base, "")
	if err != nil {
		t.Fatal(err)
	}
	boom := &pushSource{name: "boom", fn: func(ctx context.Context, push func(map[string]any)) error {
		return context.DeadlineExceeded
	}}
	if err := tr.Attach(boom); err == nil {
		t.Fatal("Source.Start error must propagate from Attach")
	}
}

func TestRemote_FullSnapshotReplacement(t *testing.T) {
	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml", "svc:\n  local: 1\n")
	tr, err := Load(base, "")
	if err != nil {
		t.Fatal(err)
	}
	tr.applyRemote(map[string]any{
		"svc": map[string]any{"local": 1, "r1": true, "r2": true},
	})
	if _, ok := tr.Raw("svc"); !ok || mustRawStr(t, tr, "svc", "r1") != true {
		t.Fatal("first snapshot not merged")
	}
	// 全量快照：消失的键即删除；本地键不受影响
	tr.applyRemote(map[string]any{
		"svc": map[string]any{"local": 1, "r2": true},
	})
	m, _ := tr.Raw("svc")
	if _, exists := m["r1"]; exists {
		t.Fatal("key gone from snapshot must be deleted")
	}
	if m["local"] != 1 {
		t.Fatalf("local layer must persist under remote, got %v", m["local"])
	}
}

func TestRemote_SitsUnderStaticLayer(t *testing.T) {
	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml", "svc:\n  a: file\n")
	tr, err := Load(base, "", Override{Key: "svc.a", Value: "flag"})
	if err != nil {
		t.Fatal(err)
	}
	tr.applyRemote(map[string]any{"svc": map[string]any{"a": "remote"}})
	if got := mustRawStr(t, tr, "svc", "a"); got != "flag" {
		t.Fatalf("static layer must sit above remote, got %v", got)
	}
}

func TestAttach_SecondPushBeforeStartReturns(t *testing.T) {
	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml", "svc:\n  a: 1\n")
	tr, err := Load(base, "")
	if err != nil {
		t.Fatal(err)
	}
	src := &pushSource{name: "multi", fn: func(ctx context.Context, push func(map[string]any)) error {
		push(map[string]any{"svc": map[string]any{"a": 2}})
		push(map[string]any{"svc": map[string]any{"a": 3}})
		<-ctx.Done()
		return nil
	}}
	if err := tr.Attach(src); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if mustRawStr(t, tr, "svc", "a") == 3 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("second snapshot not applied")
}
