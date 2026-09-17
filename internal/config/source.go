package config

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"

	"github.com/jninng/observ"
)

// Source 是远程配置提供方契约：每次配置变更以全量快照调用 push（非增量），
// ctx 取消即停止。快照中消失的节即视为删除。
type Source interface {
	Name() string
	Start(ctx context.Context, push func(map[string]any)) error
}

// Attach 启动远程源并等待首份快照合并完成后返回（同步语义：
// 组件初值因此总是完整的"本地 + 远程 + 静态层"合并结果）。
// Source.Start 返回 error 时原样返回，由装配点 fail-fast；
// 首快照之后的运行期错误由源自行处理。
func (t *Tree) Attach(src Source) error {
	first := make(chan struct{})
	var once sync.Once
	push := func(snap map[string]any) {
		t.applyRemote(snap)
		once.Do(func() { close(first) })
	}
	errCh := make(chan error, 1)
	go func() {
		if err := src.Start(t.ctx, push); err != nil {
			errCh <- err
		}
	}()
	select {
	case err := <-errCh:
		return fmt.Errorf("config: source %s: %w", src.Name(), err)
	case <-first:
		select {
		case err := <-errCh:
			return fmt.Errorf("config: source %s: %w", src.Name(), err)
		default:
			return nil
		}
	}
}

// watchFiles 监听基础与多环境文件。监听父目录并按路径过滤——
// 直监听文件会漏掉 symlink 替换（k8s ConfigMap）与编辑器原子写
//（temp+rename）两类事件。
func (t *Tree) watchFiles() {
	paths := make([]string, 0, 2)
	for _, p := range []string{t.basePath, t.envPath} {
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		paths = append(paths, filepath.Clean(abs))
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.logWarn("config file watch unavailable, hot reload disabled", slog.Any("err", err))
		return
	}
	dirs := map[string]bool{}
	for _, p := range paths {
		dirs[filepath.Dir(p)] = true
	}
	for d := range dirs {
		if err := w.Add(d); err != nil {
			t.logWarn("config file watch unavailable, hot reload disabled",
				slog.String("dir", d), slog.Any("err", err))
			w.Close()
			return
		}
	}
	go func() {
		defer w.Close()
		for {
			select {
			case <-t.ctx.Done():
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				t.onFileEvent(paths, ev)
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				t.logWarn("config file watch error", slog.Any("err", err))
			}
		}
	}()
}

func (t *Tree) onFileEvent(paths []string, ev fsnotify.Event) {
	for _, p := range paths {
		if !samePath(ev.Name, p) {
			continue
		}
		t.reload(p)
		return
	}
}

// reload 重读单个文件；解析失败记日志、维持上一有效层（全量快照语义下最终一致）。
// 原子写（temp+rename）后的首个事件可能与写方句柄竞争，故先做有界重试。
func (t *Tree) reload(path string) {
	var (
		m   map[string]any
		err error
	)
	for attempt := 0; attempt < 3; attempt++ {
		m, err = readFileLayer(path)
		if err == nil {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if err != nil {
		t.logWarn("config reload failed, keeping last valid",
			slog.String("file", path), slog.Any("err", err))
		return
	}
	if samePath(path, t.basePath) {
		t.setBaseLayer(m)
		return
	}
	t.setEnvLayer(m)
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func readFileLayer(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

func (t *Tree) logWarn(msg string, attrs ...slog.Attr) {
	// 动态读默认 logger：config 构造早于日志装配，快照会永久固定在 Noop
	observ.DefaultLogger().Log(slog.LevelWarn, msg, attrs...)
}
