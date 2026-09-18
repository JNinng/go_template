// biz.go 是业务装配入口——业务组件的接线从这里发起，无需读 Run 启动
// 时序，也不必改模板其余文件。
//
// 模板内置占位业务组件（internal/biz 的 Hello，启动输出一句日志）演示
// 完整接入链路；项目落地后以真实业务组件替换该包（保持组件约定：
// Config / Default / New / Start / Stop / 可选 ApplyConfig，零 import
// 模板），本文件的接线形态不变。组件间依赖以返回值显式传参（前一个
// 组件的返回值喂给下一个的构造参数）；meta 供需要应用元数据的组件
// 使用（如注册组件的 service name 传 meta.Name）。
package app

import (
	"go_template/internal/biz"
	"go_template/internal/config"
)

// setupBiz 逐组件 解码配置节 → 构造 → 注册生命周期（Use）；
// 解码或构造失败 → 引导失败（stderr + 退出码 1）。
func setupBiz(t *config.Tree, r *runner, meta Meta) error {
	// 占位业务：配置节 biz（缺失时以 Default 全默认值运行）；
	// Hello 无 ApplyConfig → Use 不订阅热更（演示"无热更能力则省略"形态）
	if _, err := Use(t, r, "biz", biz.Default(),
		func(c biz.Config) (*biz.Hello, error) { return biz.New(c) }); err != nil {
		return err
	}

	// 更多业务组件照此追加（greeter 形态的完整演示见 DESIGN 附录 A）：
	//
	// comp, err := Use(t, r, "my-component", mycomp.Default(),
	//     func(c mycomp.Config) (*mycomp.Comp, error) { return mycomp.New(c) })
	// if err != nil {
	//     return err
	// }
	// _ = comp // 类型化访问器喂给依赖它的下一个组件
	return nil
}
