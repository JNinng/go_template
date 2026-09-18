package app

import "go_template/internal/config"

// biz.go 是业务装配入口——业务组件的接线从这里发起，无需读 Run 启动
// 时序，也不必改模板其余文件。
//
// 业务组件本体放 internal/ 下你自己的包（如 internal/biz/server），遵循
// 组件约定（DESIGN §11：Config / Default / New / Start / Stop / 可选
// ApplyConfig），零 import 模板；在此经 app.Use（或等价手写展开）接线。
// 组件间依赖以返回值显式传参（前一个组件的返回值喂给下一个的构造参数）；
// meta 供需要应用元数据的组件使用（如注册组件的 service name 传 meta.Name）。
//
// 模板内为空实现：填入不算修改模板（与 wireSource / wire 同等待遇）。
// 原样运行时本函数为空进程保持无组件基线：打印启动行 → 静默等待信号。
func setupBiz(t *config.Tree, r *runner, meta Meta) error {
	// 接线示例（引入组件后按需改写；geeter 形态的完整演示组件见 DESIGN 附录 A）：
	//
	// comp, err := Use(t, r, "my-component", mycomp.Default(),
	//     func(c mycomp.Config) (*mycomp.Comp, error) { return mycomp.New(c) })
	// if err != nil {
	//     return err // 构造/解码失败 → 引导失败（stderr + 退出码 1）
	// }
	// _ = comp // 类型化访问器喂给依赖它的下一个组件
	return nil
}
