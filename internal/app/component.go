package app

import (
	"context"
	"fmt"

	"go_template/internal/config"
	"go_template/internal/runner"
)

// AddComponent 装配一个业务组件，一行完成五件事：
// 读配置节（以 def 为默认值）→ 构造 → 校验节名自述 → 注册启停 → 订阅配置热更（可选）。
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
	// 组件实现 Section() string（统一节名接口）即校验自述节名与接线一致，
	// 组件文档声明的节名升格为代码事实源（组件包以 SectionName 常量自述），
	// 装配点的字面量漂移在此 fail-fast。
	if s, ok := any(c).(sectioner); ok && s.Section() != section {
		var zero C
		return zero, fmt.Errorf("app: section mismatch: wired %q, component declares %q", section, s.Section())
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

// sectioner 可选的节名接口：组件实现 Section() string 即自述其配置节名
// （单一事实源是组件包导出的 SectionName 常量，方法恒返回它）。
// 结构化接口，组件零 import 即被识别；AddComponent 校验与接线节名一致。
type sectioner interface{ Section() string }
