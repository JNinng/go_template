package app

import (
	"context"

	"go_template/internal/config"
)

// wireSource 是源接线触点：远程配置源（Source）在此接入，先于日志装配。
// 模板内为空实现，填入不算修改。引导自身所需配置（如 nacos 连接参数）
// 只能来自本地层——读它时远程尚未连通（DESIGN §8.2）。
// Source 接口可由结构化类型满足：资产零 import 模板、暴露同签名方法
// 即可直传 Attach；Attach 的首快照同步语义保证返回时树已含远程层，
// 后续日志与组件的初值因此完整（format/output 等非热更字段方能由远程治理）。
func wireSource(t *config.Tree) error {
	return nil
}

// wire 是组件接线触点：逐组件 解码配置节 → 构造 → 注册生命周期 →
// （可选）Watch 热更。模板内为空实现，填入不算修改；组件间依赖以
// 显式传参表达。远程源的接入不在此时序——见 wireSource（先于日志装配）。
func wire(t *config.Tree, r *runner) error {
	return nil
}

// lifecycle 是 Use 识别组件生命周期的结构化接口（组件零 import 即被识别）。
type lifecycle interface {
	Start(context.Context) error // 起 goroutine 后立即返回；返回 nil 即可用
	Stop(context.Context) error  // 幂等可重入；ctx 携带预算，超时自行截断
}

// applier 是 Use 自动订阅节热更的识别接口：实现 ApplyConfig(Cfg) 即被订阅。
type applier[Cfg any] interface{ ApplyConfig(Cfg) error }

// Use 解码（基座 def）→ newFn 构造 → 注册生命周期；
// 组件实现 ApplyConfig(Cfg) 时自动订阅节热更（重解码仍以 def 为基座）。
// 接口由 Go 结构化类型满足——组件零 import 即被识别。
func Use[Cfg any, C lifecycle](t *config.Tree, r *runner,
	section string, def Cfg, newFn func(Cfg) (C, error)) (C, error) {
	cfg, err := config.Decode(t, section, def)
	if err != nil {
		var zero C
		return zero, err
	}
	c, err := newFn(cfg)
	if err != nil {
		var zero C
		return zero, err
	}
	r.Add(section, c.Start, c.Stop)
	if a, ok := any(c).(applier[Cfg]); ok {
		config.Watch(t, section, def, a.ApplyConfig)
	}
	return c, nil
}
