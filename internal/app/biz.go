// 业务组件的装配入口：你的组件从这里接入，无需读 Run 的其余部分。
//
// 写法：业务组件放 internal/ 下你自己的包（约定：Config / Default /
// New / Start / Stop，可选 ApplyConfig），在此用 AddComponent 接线。
// 模板内置的占位组件 internal/biz 演示了完整链路，项目落地后替换该包。
package app

import (
	"go_template/internal/biz"
	"go_template/internal/config"
	"go_template/internal/components/greeter"
	"go_template/internal/runner"

	"github.com/jninng/nacos"
)

// setupBiz 装配全部业务组件；任一组件读配置或构造失败 → 引导失败（退出码 1）。
func setupBiz(t *config.Tree, r *runner.Runner, meta Meta) error {
	// 占位业务：读 biz 节构造 Hello，启动时输出一句日志。
	// Hello 没有实现 ApplyConfig，所以改 biz 节不热更（重启生效）。
	if _, err := AddComponent(t, r, "biz", biz.Default(),
		func(c biz.Config) (*biz.Hello, error) { return biz.New(c) }); err != nil {
		return err
	}

	// greeter 演示组件：实现 ApplyConfig，配置节热更即时生效
	//（nacos demo.yaml 下发的 greeter 节覆盖本地，tick 内容随远程变）
	if _, err := AddComponent(t, r, "greeter", greeter.Default(),
		func(c greeter.Config) (*greeter.Greeter, error) { return greeter.New(c) }); err != nil {
		return err
	}

	// nacos 注册：标准生命周期组件，实例标识由装配点传参
	//（serviceName 传 meta.Name——远程下发的 app.name 即注册名）。
	// 不走配置节 + def 的组件用原语 r.Add 直接登记。
	cfg, err := config.Decode(t, "nacos", nacos.Default())
	if err != nil {
		return err
	}
	reg, err := nacos.NewReg(cfg, meta.Name, 8080)
	if err != nil {
		return err
	}
	r.Add("nacos-reg", reg.Start, reg.Stop)
	return nil
}
