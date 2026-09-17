package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 本文件走真实 fsnotify 链路：事件 → 重读 → 合并 → 总线。

func TestFileHotReload_PlainWrite(t *testing.T) {
	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml", "demo:\n  message: v1\n")
	tr, err := Load(base, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base, []byte("demo:\n  message: v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		return mustRawStr(t, tr, "demo", "message") == "v2"
	})
}

func TestFileHotReload_AtomicReplace(t *testing.T) {
	// 编辑器原子写（temp+rename）：监听父目录才能捕获
	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml", "demo:\n  message: v1\n")
	tr, err := Load(base, "")
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, "config.yaml.tmp")
	if err := os.WriteFile(tmp, []byte("demo:\n  message: v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(base); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, base); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		return mustRawStr(t, tr, "demo", "message") == "v2"
	})
}

func TestFileHotReload_MultiEnvFile(t *testing.T) {
	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml", "demo:\n  message: base\n")
	writeTemp(t, dir, "config.prod.yaml", "demo:\n  message: prod\n")
	tr, err := Load(base, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got := mustRawStr(t, tr, "demo", "message"); got != "prod" {
		t.Fatalf("env layer not applied at load, got %v", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.prod.yaml"),
		[]byte("demo:\n  message: prod2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		return mustRawStr(t, tr, "demo", "message") == "prod2"
	})
}

func TestFileHotReload_ParseFailureKeepsLastValid(t *testing.T) {
	dir := t.TempDir()
	base := writeTemp(t, dir, "config.yaml", "demo:\n  message: good\n")
	tr, err := Load(base, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base, []byte("demo: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	if got := mustRawStr(t, tr, "demo", "message"); got != "good" {
		t.Fatalf("broken yaml must keep last valid tree, got %v", got)
	}
	// 恢复后收敛
	if err := os.WriteFile(base, []byte("demo:\n  message: fixed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		return mustRawStr(t, tr, "demo", "message") == "fixed"
	})
}
