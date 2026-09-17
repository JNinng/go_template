package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go_template/internal/config"
)

// stubComp 是符合组件约定的最小原生组件（仅测试内存在）。
type stubConfig struct {
	Msg string `yaml:"message"`
	N   int    `yaml:"interval_seconds"`
}

func stubDefault() stubConfig { return stubConfig{Msg: "hello", N: 10} }

type stubComp struct {
	mu      sync.Mutex
	cfg     stubConfig
	applied []stubConfig
	stopped int
}

func newStub(cfg stubConfig) (*stubComp, error) {
	if cfg.N <= 0 {
		return nil, errors.New("stub: interval_seconds must be > 0")
	}
	return &stubComp{cfg: cfg}, nil
}

func (s *stubComp) Start(context.Context) error { return nil }

func (s *stubComp) Stop(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped++
	return nil
}

func (s *stubComp) ApplyConfig(cfg stubConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cfg.N <= 0 {
		return errors.New("stub: reject non-positive interval_seconds")
	}
	s.cfg = cfg
	s.applied = append(s.applied, cfg)
	return nil
}

func newUseTree(t *testing.T, sectionContent string) (*config.Tree, string) {
	t.Helper()
	dir := t.TempDir()
	base := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(base, []byte(sectionContent), 0o644); err != nil {
		t.Fatal(err)
	}
	tr, err := config.Load(base, "")
	if err != nil {
		t.Fatal(err)
	}
	return tr, dir
}

func TestUse_DecodeConstructRegister(t *testing.T) {
	tr, _ := newUseTree(t, "stub:\n  message: hi\n  interval_seconds: 3\n")
	r := new(runner)

	c, err := Use(tr, r, "stub", stubDefault(),
		func(c stubConfig) (*stubComp, error) { return newStub(c) })
	if err != nil {
		t.Fatal(err)
	}
	if len(r.entries) != 1 || r.entries[0].name != "stub" {
		t.Fatalf("lifecycle not registered: %+v", r.entries)
	}
	if _, err := r.startAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.shutdown(1); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped != 1 {
		t.Fatalf("stop count = %d, want 1", c.stopped)
	}
}

func TestUse_AutoSubscribesApplyConfig(t *testing.T) {
	tr, _ := newUseTree(t, "stub:\n  message: hi\n  interval_seconds: 3\n")
	r := new(runner)
	c, err := Use(tr, r, "stub", stubDefault(),
		func(cfg stubConfig) (*stubComp, error) { return newStub(cfg) })
	if err != nil {
		t.Fatal(err)
	}

	// 收敛首调
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		n := len(c.applied)
		c.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.mu.Lock()
	first := len(c.applied)
	c.mu.Unlock()
	if first != 1 {
		t.Fatalf("convergence apply count = %d, want 1", first)
	}

	// 节热更 → ApplyConfig 收到重解码值（仍以 Default 为基座）
	src := &pushSrc{fn: func(ctx context.Context, push func(map[string]any)) error {
		push(map[string]any{"stub": map[string]any{"message": "hot"}})
		<-ctx.Done()
		return nil
	}}
	if err := tr.Attach(src); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		cfg := c.cfg
		c.mu.Unlock()
		if cfg.Msg == "hot" && cfg.N == 3 {
			return // 远程快照缺的键由下层（文件 interval_seconds: 3）供值
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("ApplyConfig did not receive hot-reloaded config")
}

func TestUse_DecodeError(t *testing.T) {
	tr, _ := newUseTree(t, "stub:\n  unknown_key: 1\n")
	if _, err := Use(tr, new(runner), "stub", stubDefault(),
		func(stubConfig) (*stubComp, error) { return &stubComp{}, nil }); err == nil {
		t.Fatal("unknown key must fail decode")
	}
}

func TestUse_NewError(t *testing.T) {
	tr, _ := newUseTree(t, "stub:\n  interval_seconds: 0\n")
	r := new(runner)
	if _, err := Use(tr, r, "stub", stubDefault(),
		func(cfg stubConfig) (*stubComp, error) { return newStub(cfg) }); err == nil {
		t.Fatal("New failure must propagate")
	}
	if len(r.entries) != 0 {
		t.Fatal("failed construct must not register lifecycle")
	}
}

func TestLoadMeta_NameRequiredAndEnvResolution(t *testing.T) {
	dir := t.TempDir()
	mk := func(appYaml string) *config.Tree {
		p := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(p, []byte(appYaml), 0o644); err != nil {
			t.Fatal(err)
		}
		tr, err := config.Load(p, "")
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}

	if _, _, err := loadMeta(mk("app:\n  env: prod\n"), ""); err == nil {
		t.Fatal("missing name must fail-fast")
	}

	m, eff, err := loadMeta(mk("app:\n  name: demo\n  env: prod\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "demo" || eff != "prod" {
		t.Fatalf("declared env should stand, got %+v eff=%q", m, eff)
	}

	_, eff, err = loadMeta(mk("app:\n  name: demo\n  env: prod\n"), "staging")
	if err != nil {
		t.Fatal(err)
	}
	if eff != "staging" {
		t.Fatalf("explicit env input must win, got %q", eff)
	}
}
