// Package config 提供配置加载、合并、读取与热更订阅。
// 分层契约见 docs/DESIGN.md §8：代码默认值 < 基础文件 < 多环境文件
// < 远程源 < from_env 绑定（静态）< flag 覆盖（静态）。
package config

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Override 是置于合并栈顶的静态点路径键（run 命令传 --log-level 用）。
type Override struct {
	Key   string // 点路径键，如 "log.level"
	Value any
}

// Tree 持有全部配置层，对外提供读取、订阅与远程源接入；并发安全。
type Tree struct {
	mu sync.RWMutex

	base    map[string]any // 基础文件层
	multiEv map[string]any // 多环境文件层（未选定则为 nil）
	remote  map[string]any // 远程源最后有效快照（未接入则为 nil）

	bindings  []binding  // from_env 静态绑定，启动时一次性解析、其后冻结
	overrides []Override // flag 等静态覆盖，位于栈顶

	current map[string]any // 合并结果快照，每次重建整体替换、旧快照不可变

	subs []*subscription

	basePath string
	envPath  string

	ctx    context.Context
	cancel context.CancelFunc
}

// Load 读两层本地文件 → 收集 from_env → 应用静态覆盖，并启动文件监听。
// env 非空时多环境文件必须存在（显式选定而缺失 → fail-fast）。
func Load(configPath, env string, overrides ...Override) (*Tree, error) {
	base, err := readFileLayer(configPath)
	if err != nil {
		return nil, fmt.Errorf("config: load %s: %w", configPath, err)
	}
	envPath := ""
	var multiEnv map[string]any
	if env != "" {
		envPath = InsertEnvSuffix(configPath, env)
		multiEnv, err = readFileLayer(envPath)
		if err != nil {
			return nil, fmt.Errorf("config: load %s: %w", envPath, err)
		}
	}

	local := deepCopyMap(base)
	if multiEnv != nil {
		local = deepMerge(local, deepCopyMap(multiEnv))
	}
	bindings := collectBindings(local)

	t := &Tree{
		base:      base,
		multiEv:   multiEnv,
		bindings:  bindings,
		overrides: overrides,
		basePath:  configPath,
		envPath:   envPath,
	}
	t.ctx, t.cancel = context.WithCancel(context.Background())
	t.mu.Lock()
	t.current = t.mergeLocked()
	t.mu.Unlock()
	t.watchFiles()
	return t, nil
}

// Raw 返回配置节原样视图（含 from_env 保留键）。
func (t *Tree) Raw(section string) (map[string]any, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.sectionLocked(section)
}

func (t *Tree) sectionLocked(section string) (map[string]any, bool) {
	if t.current == nil {
		return nil, false
	}
	v, ok := t.current[section]
	if !ok {
		return nil, false
	}
	m, ok := v.(map[string]any)
	return m, ok
}

// Decode 泛型解码：base 为默认值基座，节内键覆盖之。
// 严格解码（未知键报错）；节缺失返回 base 原样；from_env 保留键在解码前剔除。
func Decode[T any](t *Tree, section string, base T) (T, error) {
	return decodeSection(t, section, base)
}

func decodeSection[T any](t *Tree, section string, base T) (T, error) {
	t.mu.RLock()
	m, ok := t.sectionLocked(section)
	t.mu.RUnlock()
	if !ok {
		return base, nil
	}
	data, err := yaml.Marshal(stripReserved(m))
	if err != nil {
		var zero T
		return zero, fmt.Errorf("config: encode section %q: %w", section, err)
	}
	out := base
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&out); err != nil {
		var zero T
		return zero, fmt.Errorf("config: decode section %q: %w", section, err)
	}
	return out, nil
}

// Dump 把组件默认值渲染为可粘贴的 yaml 配置节。
func Dump(w io.Writer, section string, cfg any) error {
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	defer enc.Close()
	if err := enc.Encode(map[string]any{section: cfg}); err != nil {
		return fmt.Errorf("config: dump section %q: %w", section, err)
	}
	return nil
}

// InsertEnvSuffix 在扩展名前插入 .<env>：config.yaml + prod → config.prod.yaml。
func InsertEnvSuffix(path, env string) string {
	ext := filepath.Ext(path)
	return strings.TrimSuffix(path, ext) + "." + env + ext
}

// mergeLocked 由当前各层重算合并树，须持有写锁；返回全新快照。
// 静态层（from_env 绑定、flag 覆盖）每次重建重新套用——运行时覆盖与启动时同构。
func (t *Tree) mergeLocked() map[string]any {
	cur := deepCopyMap(t.base)
	if t.multiEv != nil {
		cur = deepMerge(cur, deepCopyMap(t.multiEv))
	}
	if t.remote != nil {
		cur = deepMerge(cur, deepCopyMap(t.remote))
	}
	for _, b := range t.bindings {
		setPath(cur, b.path, b.value)
	}
	for _, o := range t.overrides {
		setPath(cur, strings.Split(o.Key, "."), o.Value)
	}
	return cur
}

func (t *Tree) setBaseLayer(m map[string]any) {
	t.mu.Lock()
	t.base = m
	t.current = t.mergeLocked()
	t.mu.Unlock()
	t.notifyAll()
}

func (t *Tree) setEnvLayer(m map[string]any) {
	t.mu.Lock()
	t.multiEv = m
	t.current = t.mergeLocked()
	t.mu.Unlock()
	t.notifyAll()
}

// applyRemote 以全量快照替换远程层：快照中消失的节即视为删除。
func (t *Tree) applyRemote(snap map[string]any) {
	t.mu.Lock()
	t.remote = snap
	t.current = t.mergeLocked()
	t.mu.Unlock()
	t.notifyAll()
}

// deepMerge 就地深合并 src 到 dst：map 递归合并，标量与数组整体覆盖。
func deepMerge(dst, src map[string]any) map[string]any {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				deepMerge(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
	return dst
}

// deepCopyMap 复制嵌套 map 结构；叶子的标量与切片只读共享。
func deepCopyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			out[k] = deepCopyMap(sub)
			continue
		}
		out[k] = v
	}
	return out
}
