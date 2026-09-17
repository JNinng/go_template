package config

import (
	"os"
	"strconv"
)

// binding 是一条已解析的 from_env 静态覆盖：path 指向节内点路径，value 为
// 启动时读取的环境变量值（尽力类型推断）。声明仅在 Load 时收集一次，
// 其后冻结——声明本身的热更不生效（重启生效）。
type binding struct {
	path  []string
	value any
}

// collectBindings 从合并后的本地层（基础 + 多环境文件）收集全部 from_env
// 声明并解析环境变量值。环境变量未设或空串不生效；远程层不参与
//（实例级绑定属部署决策，不下发自远程）。
func collectBindings(local map[string]any) []binding {
	var bs []binding
	for sec, v := range local {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		decl, ok := m["from_env"].(map[string]any)
		if !ok {
			continue
		}
		walkDecl(decl, []string{sec}, func(path []string, envName string) {
			val, ok := os.LookupEnv(envName)
			if !ok || val == "" {
				return
			}
			bs = append(bs, binding{path: path, value: inferValue(val)})
		})
	}
	return bs
}

// walkDecl 遍历 from_env 声明树：值为逐层 map，叶子为环境变量名。
func walkDecl(m map[string]any, prefix []string, fn func(path []string, envName string)) {
	for k, v := range m {
		if name, ok := v.(string); ok {
			path := make([]string, 0, len(prefix)+1)
			path = append(path, prefix...)
			fn(append(path, k), name)
			continue
		}
		if sub, ok := v.(map[string]any); ok {
			next := make([]string, 0, len(prefix)+1)
			next = append(next, prefix...)
			walkDecl(sub, append(next, k), fn)
		}
	}
}

// inferValue 尽力类型推断：bool → int → float64，否则字符串。
func inferValue(s string) any {
	switch s {
	case "true":
		return true
	case "false":
		return false
	}
	if i, err := strconv.Atoi(s); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}

// setPath 沿点路径写入值，途中缺失的中间层以空 map 补齐。
func setPath(m map[string]any, path []string, v any) {
	for _, k := range path[:len(path)-1] {
		next, ok := m[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[k] = next
		}
		m = next
	}
	m[path[len(path)-1]] = v
}

// stripReserved 返回剔除 from_env 保留键后的节副本（解码前调用）。
func stripReserved(m map[string]any) map[string]any {
	out := deepCopyMap(m)
	delete(out, "from_env")
	return out
}
