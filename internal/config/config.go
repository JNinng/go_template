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
	Value any    // 覆盖值，直接替换合并树对应键
}

// Tree 持有全部配置层，对外提供读取、订阅与远程源接入；并发安全。
type Tree struct {
	mu sync.RWMutex // 保护以下全部字段；current 只整体替换、旧快照不可变

	base    map[string]any // 基础文件层
	multiEv map[string]any // 多环境文件层（未选定则为 nil）
	remote  map[string]any // 远程源最后有效快照（未接入则为 nil）

	bindings  []binding  // from_env 静态绑定，启动时一次性解析、其后冻结
	overrides []Override // flag 等静态覆盖，位于栈顶

	current map[string]any // 合并结果快照，每次重建整体替换、旧快照不可变

	subs []*subscription // 活跃订阅（热更总线）

	basePath string // 基础文件绝对化前路径，用于监听事件归属
	envPath  string // 多环境文件路径，空串表示未选定

	ctx    context.Context // 文件监听与远程源的生命周期（随进程）
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
// 返回的是不可变快照，可安全持有。
func (t *Tree) Raw(section string) (map[string]any, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.sectionLocked(section)
}

// sectionLocked 读当前合并树的指定节；调用方须持有读锁或写锁。
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

// Section 是配置节的运行时句柄：绑定（树，节名，默认值基座）三元组，
// 支持任意时刻拉取该节的当前合并生效值。Get 的 pull 语义与 Watch 的
// push 语义互补（偶发读取不必常驻订阅）；与 Decode 走同一解码路径，
// 三者结果恒一致。并发安全；经 Bind 构造，零值不可用。
type Section[T any] struct {
	t    *Tree
	name string
	base T
}

// Bind 把节绑定为运行时句柄：只登记三元组，不读配置、不失败
// （解析推迟到 Get）。name 建议引组件的 SectionName 常量，避免字面量漂移。
func Bind[T any](t *Tree, name string, base T) *Section[T] {
	return &Section[T]{t: t, name: name, base: base}
}

// Name 返回节名。
func (s *Section[T]) Name() string { return s.name }

// Get 取该节当前合并生效值：严格解码（未知键报错）、节缺失回落 base，
// 与 Decode 同一路径（decodeSection）。
func (s *Section[T]) Get() (T, error) { return decodeSection(s.t, s.name, s.base) }

// decodeSection 是 Decode 的实现体，也是 Watch 投递时的重解码入口：
// 两者走同一路径，收敛首调与 Decode 结果因此恒一致。
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

// setBaseLayer 替换基础文件层并重建合并树（文件热更入口）。
func (t *Tree) setBaseLayer(m map[string]any) {
	t.mu.Lock()
	t.base = m
	t.current = t.mergeLocked()
	t.mu.Unlock()
	t.notifyAll()
}

// setEnvLayer 替换多环境文件层并重建合并树（文件热更入口）。
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
