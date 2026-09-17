package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func mustRawStr(t *testing.T, tr *Tree, section, key string) any {
	t.Helper()
	m, ok := tr.Raw(section)
	if !ok {
		t.Fatalf("section %q missing", section)
	}
	v, ok := m[key]
	if !ok {
		t.Fatalf("key %q missing in section %q", key, section)
	}
	return v
}

func TestLoad_MergesBaseAndMultiEnv(t *testing.T) {
	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml", "svc:\n  a: 1\n  nested:\n    x: 1\n    y: 1\n")
	writeTemp(t, dir, "config.prod.yaml", "svc:\n  a: 2\n  nested:\n    y: 2\n")

	tr, err := Load(base, "prod", Override{Key: "svc.a", Value: 9})
	if err != nil {
		t.Fatal(err)
	}

	// 静态覆盖在栈顶
	if got := mustRawStr(t, tr, "svc", "a"); got != 9 {
		t.Fatalf("override should win, got %v", got)
	}
	nested := mustRawStr(t, tr, "svc", "nested").(map[string]any)
	if nested["x"] != 1 || nested["y"] != 2 {
		t.Fatalf("deep merge broken: %v", nested)
	}
}

func TestLoad_MultiEnvFileOnlyViaExplicitEnv(t *testing.T) {
	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml", "svc:\n  a: 1\n")
	writeTemp(t, dir, "config.prod.yaml", "svc:\n  a: 2\n")

	tr, err := Load(base, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := mustRawStr(t, tr, "svc", "a"); got != 1 {
		t.Fatalf("env file must not load without explicit env, got %v", got)
	}

	tr, err = Load(base, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got := mustRawStr(t, tr, "svc", "a"); got != 2 {
		t.Fatalf("env file should overlay, got %v", got)
	}
}

func TestLoad_FailFast(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(filepath.Join(dir, "missing.yaml"), ""); err == nil {
		t.Fatal("missing base file must fail")
	}
	base := writeTemp(t, dir, "config.yaml", "svc:\n  a: 1\n")
	if _, err := Load(base, "prod"); err == nil {
		t.Fatal("explicit env with missing file must fail")
	}
	if _, err := Load(filepath.Join(dir, "broken.yaml"), ""); err == nil {
		t.Fatal("missing file must fail")
	}
}

func TestLoad_BrokenYamlFails(t *testing.T) {
	dir := t.TempDir()
	p := writeTemp(t, dir, "config.yaml", "svc: [\n")
	if _, err := Load(p, ""); err == nil {
		t.Fatal("broken yaml must fail at load")
	}
}

func TestInsertEnvSuffix(t *testing.T) {
	cases := []struct{ path, env, want string }{
		{"config.yaml", "prod", "config.prod.yaml"},
		{"configs/config.yaml", "staging", "configs/config.staging.yaml"},
		{"configs/a.b.yaml", "prod", "configs/a.b.prod.yaml"},
		{"cfg", "prod", "cfg.prod"},
	}
	for _, c := range cases {
		if got := InsertEnvSuffix(c.path, c.env); got != c.want {
			t.Errorf("InsertEnvSuffix(%q, %q) = %q, want %q", c.path, c.env, got, c.want)
		}
	}
}

type demoCfg struct {
	Msg string `yaml:"message"`
	N   int    `yaml:"interval_seconds"`
}

func TestDecode_MissingSectionReturnsBase(t *testing.T) {
	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml", "other:\n  a: 1\n")
	tr, err := Load(base, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(tr, "demo", demoCfg{Msg: "hello", N: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg != "hello" || got.N != 10 {
		t.Fatalf("missing section must return base, got %+v", got)
	}
}

func TestDecode_SectionKeysOverrideBase(t *testing.T) {
	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml", "demo:\n  interval_seconds: 5\n")
	tr, err := Load(base, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(tr, "demo", demoCfg{Msg: "hello", N: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg != "hello" || got.N != 5 {
		t.Fatalf("want base defaults + section overrides, got %+v", got)
	}
}

func TestDecode_StrictUnknownKey(t *testing.T) {
	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml", "demo:\n  nope: 1\n")
	tr, err := Load(base, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(tr, "demo", demoCfg{}); err == nil {
		t.Fatal("unknown key must fail strict decode")
	}
}

func TestDecode_StripsFromEnvReservedKey(t *testing.T) {
	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml", "demo:\n  from_env:\n    message: DEMO_MSG\n  message: hi\n")
	tr, err := Load(base, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(tr, "demo", demoCfg{N: 1}); err != nil {
		t.Fatalf("from_env key must be stripped before decode: %v", err)
	}
}

func TestDump_RendersSection(t *testing.T) {
	var buf bytes.Buffer
	if err := Dump(&buf, "demo", demoCfg{Msg: "hello", N: 10}); err != nil {
		t.Fatal(err)
	}
	want := "demo:\n  message: hello\n  interval_seconds: 10\n"
	if buf.String() != want {
		t.Fatalf("Dump output:\n%q\nwant:\n%q", buf.String(), want)
	}
}
