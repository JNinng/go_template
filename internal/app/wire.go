package app

import (
	"context"

	"go_template/internal/config"
)

// wire 是装配点：引入组件的唯一触点。模板内为空实现；
// 填入组件接线不算修改模板。远程配置源在此以 Source 适配接入
//（Attach 先于一切组件装配），组件间依赖以显式传参表达。
func wire(t *config.Tree, r *runner) error {
	return nil
}

type lifecycle interface {
	Start(context.Context) error
	Stop(context.Context) error
}

type applier[Cfg any] interface{ ApplyConfig(Cfg) error }

// Use：解码（基座 def）→ newFn 构造 → 注册生命周期；
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
