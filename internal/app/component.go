package app

import (
	"context"

	"go_template/internal/config"
	"go_template/internal/runner"
)

// AddComponent 装配一个业务组件，一行完成四件事：
// 读配置节（以 def 为默认值）→ 构造 → 注册启停 → 订阅配置热更（可选）。
//
// 组件无需 import 本模板：只要有 Start/Stop 方法即可（Go 结构化接口），
// newFn 通常是一行闭包。配置节缺失时组件以 def 全默认值运行。
//
// 与 runner 包的关系：Runner.Add 只登记一对启停函数（原语）；
// AddComponent 多做了读配置和构造，业务装配一律用它。
func AddComponent[Cfg any, C lifecycle](t *config.Tree, r *runner.Runner,
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
	// 组件实现了 ApplyConfig(Cfg) 就自动订阅该节热更；没实现就跳过
	if a, ok := any(c).(applier[Cfg]); ok {
		config.Watch(t, section, def, a.ApplyConfig)
	}
	return c, nil
}

// lifecycle 组件需要满足的最小接口：能启动、能停止。
type lifecycle interface {
	Start(context.Context) error // 起 goroutine 后立即返回；返回 nil 即可用
	Stop(context.Context) error  // 幂等可重入；ctx 携带停机预算
}

// applier 可选的热更接口：组件实现 ApplyConfig(Cfg) 即被订阅配置变更。
type applier[Cfg any] interface{ ApplyConfig(Cfg) error }
