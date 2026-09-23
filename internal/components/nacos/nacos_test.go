package nacos

import (
	"context"
	"strings"
	"testing"
	"time"
)

// deadAddr：本机无监听端口，连接立即拒绝（离线可测不可达路径）。
const deadAddr = "127.0.0.1:1"

// startAsync 在后台跑 Start，返回结果通道与 push 收集通道。
func startAsync(t *testing.T, c *CfgClient) (<-chan error, <-chan map[string]any, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errCh := make(chan error, 1)
	snapCh := make(chan map[string]any, 16)
	go func() { errCh <- c.Start(ctx, func(m map[string]any) { snapCh <- m }) }()
	return errCh, snapCh, cancel
}

func waitPush(t *testing.T, snapCh <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case m := <-snapCh:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("no snapshot pushed within timeout")
		return nil
	}
}

func TestDefault(t *testing.T) {
	d := Default()
	// 配置中心缺省启用 + fail；注册角色级开关缺省禁用（启用需实例端口）
	if !d.Config.Enabled || d.Config.Unreachable != "fail" || d.Registrar.Unreachable != "fail" {
		t.Errorf("Default() = %+v（fail-fast 缺省）", d)
	}
	if d.Registrar.Enabled {
		t.Error("registrar.enabled must default to false")
	}
}

func TestNewCfgClient_Validation(t *testing.T) {
	cfg := Default()

	if _, err := NewCfgClient(cfg); err != nil {
		t.Fatalf("valid default config rejected: %v", err)
	}

	bad := cfg
	bad.Config.Unreachable = "skip"
	if _, err := NewCfgClient(bad); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("invalid policy: %v", err)
	}

	for _, addr := range []string{"", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:abc"} {
		bad := cfg
		bad.Config.Addr = addr
		// 地址畸形不经策略归化：disable 下同样必须 fail-fast
		bad.Config.Unreachable = "disable"
		if _, err := NewCfgClient(bad); err == nil {
			t.Errorf("addr %q must be rejected", addr)
		}
	}
}

func TestNewReg_Validation(t *testing.T) {
	cfg := Default()

	// 禁用态：实例标识可缺省（旁路实例）
	if _, err := NewReg(cfg, "", 0); err != nil {
		t.Fatalf("disabled registrar must construct: %v", err)
	}

	// 启用态：service_name 与 port 必填
	on := cfg
	on.Registrar.Enabled = true
	if _, err := NewReg(on, "", 8080); err == nil || !strings.Contains(err.Error(), "service_name") {
		t.Errorf("missing service_name: %v", err)
	}
	if _, err := NewReg(on, "svc", 0); err == nil || !strings.Contains(err.Error(), "port") {
		t.Errorf("missing port: %v", err)
	}

	// 地址畸形：无论策略都 fail-fast
	on.Registrar.Addr = "127.0.0.1:abc"
	on.Registrar.Unreachable = "disable"
	if _, err := NewReg(on, "svc", 8080); err == nil {
		t.Error("malformed addr must be rejected regardless of policy")
	}
}

func TestCfgClient_DisabledRole(t *testing.T) {
	cfg := Default()
	cfg.Config.Enabled = false
	c, err := NewCfgClient(cfg)
	if err != nil {
		t.Fatal(err)
	}

	errCh, snapCh, cancel := startAsync(t, c)
	got := waitPush(t, snapCh)
	if len(got) != 0 {
		t.Fatalf("disabled role must push empty snapshot, got %v", got)
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("disabled role Start must return nil after cancel, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after cancel")
	}
}

func TestCfgClient_UnreachableFail(t *testing.T) {
	cfg := Default()
	cfg.Config.Addr = deadAddr
	cfg.Config.Unreachable = "fail"
	c, err := NewCfgClient(cfg)
	if err != nil {
		t.Fatal(err)
	}

	errCh, _, _ := startAsync(t, c)
	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "unreachable") {
			t.Fatalf("want fail-fast error, got %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Start did not fail within timeout")
	}
}

func TestCfgClient_UnreachableDisable(t *testing.T) {
	cfg := Default()
	cfg.Config.Addr = deadAddr
	cfg.Config.Unreachable = "disable"
	c, err := NewCfgClient(cfg)
	if err != nil {
		t.Fatal(err)
	}

	errCh, snapCh, cancel := startAsync(t, c)
	got := waitPush(t, snapCh)
	if len(got) != 0 {
		t.Fatalf("disable policy must push empty snapshot, got %v", got)
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("disable policy must degrade (nil), got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after cancel")
	}
}

func TestParseContent(t *testing.T) {
	m, err := parseContent("svc:\n  a: 1\n  nested:\n    b: true\n")
	if err != nil {
		t.Fatal(err)
	}
	svc := m["svc"].(map[string]any)
	if svc["a"] != 1 || svc["nested"].(map[string]any)["b"] != true {
		t.Fatalf("parse broken: %v", m)
	}

	if m, err := parseContent(""); err != nil || len(m) != 0 {
		t.Fatalf("empty content must be empty snapshot: %v, %v", m, err)
	}
	if _, err := parseContent("svc: [\n"); err == nil {
		t.Fatal("malformed content must error")
	}
}

func TestReg_DisabledRole(t *testing.T) {
	cfg := Default() // registrar.enabled 缺省 false
	r, err := NewReg(cfg, "svc", 8080)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("disabled registrar Start must be no-op, got %v", err)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("disabled registrar Stop must be no-op, got %v", err)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("Stop must be idempotent, got %v", err)
	}
}

func TestReg_UnreachableFail(t *testing.T) {
	cfg := Default()
	cfg.Registrar.Enabled = true
	cfg.Registrar.Addr = deadAddr
	r, err := NewReg(cfg, "svc", 8080)
	if err != nil {
		t.Fatal(err)
	}
	err = r.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("want fail-fast error, got %v", err)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("Stop after failed Start: %v", err)
	}
}

func TestReg_UnreachableDisable(t *testing.T) {
	cfg := Default()
	cfg.Registrar.Enabled = true
	cfg.Registrar.Addr = deadAddr
	cfg.Registrar.Unreachable = "disable"
	r, err := NewReg(cfg, "svc", 8080)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("disable policy must skip registration, got %v", err)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("Stop on skipped registrar: %v", err)
	}
}

func TestLocalIP_TargetPreferred(t *testing.T) {
	// 探测目标为目标服务地址（不依赖外部路由，无外网环境同样可探测）
	ip, err := localIP("127.0.0.1:8848")
	if err != nil {
		t.Fatalf("localIP via loopback target: %v", err)
	}
	if ip == "" {
		t.Error("localIP returned empty ip")
	}
}

// TestSection_SelfDeclaration 钉住节名契约：两个角色的组件自述与文档
// 声明的节名恒一致（字面量漂移在此暴露，而非运行期才被装配校验发现）。
func TestSection_SelfDeclaration(t *testing.T) {
	cc := &CfgClient{}
	reg := &Reg{}
	if cc.Section() != SectionName || reg.Section() != SectionName || SectionName != "nacos" {
		t.Fatalf("section self-declaration drifted: cfg=%q reg=%q const=%q",
			cc.Section(), reg.Section(), SectionName)
	}
}
