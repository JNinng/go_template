package config

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
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
	// Name 返回源名称，用于日志与错误归因（如 "nacos"）。
	Name() string
	// Start 阻塞或内部自管；每次配置变更以全量快照调用 push。
	// 返回 error 即视为源启动失败（由 Attach 原样上抛）。
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
// （temp+rename）两类事件。
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
		observ.DefaultLogger().Log(context.Background(), slog.LevelError, "config_file_watch_init_failed",
			slog.Any("error", err), slog.String("stack", string(debug.Stack())))
		return
	}
	dirs := map[string]bool{}
	for _, p := range paths {
		dirs[filepath.Dir(p)] = true
	}
	for d := range dirs {
		if err := w.Add(d); err != nil {
			observ.DefaultLogger().Log(context.Background(), slog.LevelError, "config_file_watch_init_failed",
				slog.String("watch_dir", d), slog.Any("error", err),
				slog.String("stack", string(debug.Stack())))
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
				observ.DefaultLogger().Log(context.Background(), slog.LevelError, "config_file_watch_failed",
					slog.Any("error", err), slog.String("stack", string(debug.Stack())))
			}
		}
	}()
}

// onFileEvent 按监听路径过滤事件：命中即触发对应文件重读。
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
		observ.DefaultLogger().Log(context.Background(), slog.LevelError, "config_reload_failed",
			slog.String("file_path", path), slog.Any("error", err),
			slog.String("stack", string(debug.Stack())))
		return
	}
	if samePath(path, t.basePath) {
		t.setBaseLayer(m)
		return
	}
	t.setEnvLayer(m)
}

// samePath 比较两个路径是否指向同一文件（Windows 大小写不敏感）。
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// readFileLayer 读取并解析单个 yaml 文件为一层配置；空文件视为空层。
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
