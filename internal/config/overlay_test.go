package config

import (
	"os"
	"testing"
)

func TestFromEnv_OverridesWithInference(t *testing.T) {
	t.Setenv("FE_STR", "secret")
	t.Setenv("FE_INT", "42")
	t.Setenv("FE_FLOAT", "1.5")
	t.Setenv("FE_BOOL", "true")

	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml", `
redis:
  host: localhost
  from_env:
    password: FE_STR
    pool:
      max: FE_INT
      ratio: FE_FLOAT
      debug: FE_BOOL
`)
	tr, err := Load(base, "")
	if err != nil {
		t.Fatal(err)
	}
	m, ok := tr.Raw("redis")
	if !ok {
		t.Fatal("redis section missing")
	}
	if m["password"] != "secret" {
		t.Fatalf("password = %v, want string", m["password"])
	}
	pool := m["pool"].(map[string]any)
	if pool["max"] != 42 {
		t.Fatalf("max = %v (%T), want int 42", pool["max"], pool["max"])
	}
	if pool["ratio"] != 1.5 {
		t.Fatalf("ratio = %v (%T), want float64 1.5", pool["ratio"], pool["ratio"])
	}
	if pool["debug"] != true {
		t.Fatalf("debug = %v (%T), want bool true", pool["debug"], pool["debug"])
	}
}

func TestFromEnv_EmptyOrUnsetNotApplied(t *testing.T) {
	t.Setenv("FE_EMPTY", "")
	os.Unsetenv("FE_UNSET")

	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml", `
redis:
  password: file-value
  from_env:
    password: FE_EMPTY
    host: FE_UNSET
`)
	tr, err := Load(base, "")
	if err != nil {
		t.Fatal(err)
	}
	m, _ := tr.Raw("redis")
	if m["password"] != "file-value" {
		t.Fatalf("empty env must not override, got %v", m["password"])
	}
	if m["host"] != nil {
		t.Fatalf("unset env must not create key, got %v", m["host"])
	}
}

func TestFromEnv_DeclarationsFrozenAfterLoad(t *testing.T) {
	// 声明的收集仅在 Load 时一次：其后设置环境变量不生效（重启生效）
	os.Unsetenv("FE_LATE")
	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml", `
redis:
  from_env:
    password: FE_LATE
`)
	tr, err := Load(base, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FE_LATE", "late")
	if _, ok := tr.Raw("redis"); !ok {
		t.Fatal("redis section missing")
	}
	m, _ := tr.Raw("redis")
	if m["password"] != nil {
		t.Fatalf("late env must not apply, got %v", m["password"])
	}
}

func TestFromEnv_RemoteDeclarationsIgnored(t *testing.T) {
	t.Setenv("FE_REMOTE", "evil")
	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml", "svc:\n  a: 1\n")
	tr, err := Load(base, "")
	if err != nil {
		t.Fatal(err)
	}
	// 远程下发的 from_env 声明被忽略：不产生绑定
	tr.applyRemote(map[string]any{
		"svc": map[string]any{
			"a": 2,
			"from_env": map[string]any{
				"a": "FE_REMOTE",
			},
		},
	})
	m, _ := tr.Raw("svc")
	if m["a"] != 2 {
		t.Fatalf("remote value must stand (no binding), got %v", m["a"])
	}
}

func TestStaticLayer_SurvivesRemoteAndFileChanges(t *testing.T) {
	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml", "log:\n  level: info\n")
	tr, err := Load(base, "", Override{Key: "log.level", Value: "warn"})
	if err != nil {
		t.Fatal(err)
	}
	if got := mustRawStr(t, tr, "log", "level"); got != "warn" {
		t.Fatalf("flag override should be on top, got %v", got)
	}
	// 远程与文件变更都在静态层之下，树重建后静态层重新套用
	tr.applyRemote(map[string]any{"log": map[string]any{"level": "debug"}})
	if got := mustRawStr(t, tr, "log", "level"); got != "warn" {
		t.Fatalf("static layer must survive remote change, got %v", got)
	}
}

func TestInferValue(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"true", true}, {"false", false},
		{"7", 7}, {"-3", -3},
		{"1.5", 1.5},
		{"hello", "hello"},
		{"1.2.3", "1.2.3"},
	}
	for _, c := range cases {
		if got := inferValue(c.in); got != c.want {
			t.Errorf("inferValue(%q) = %v (%T), want %v", c.in, got, got, c.want)
		}
	}
}

func TestCollectBindings_NestedAndMultipleSections(t *testing.T) {
	t.Setenv("CB_A", "1")
	t.Setenv("CB_B", "x")
	local := map[string]any{
		"redis": map[string]any{
			"from_env": map[string]any{
				"password": "CB_B",
				"pool":     map[string]any{"max": "CB_A"},
			},
		},
		"plain":          map[string]any{"k": "v"},
		"scalar_section": "not-a-map",
	}
	bs := collectBindings(local)
	if len(bs) != 2 {
		t.Fatalf("want 2 bindings, got %d: %+v", len(bs), bs)
	}
	byLen := map[int]binding{}
	for _, b := range bs {
		byLen[len(b.path)] = b
	}
	if b := byLen[2]; b.path[0] != "redis" || b.path[1] != "password" || b.value != "x" {
		t.Errorf("password binding wrong: %+v", b)
	}
	if b := byLen[3]; b.path[2] != "max" || b.value != 1 {
		t.Errorf("pool.max binding wrong: %+v", b)
	}
}
