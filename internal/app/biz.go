// 业务组件的装配入口：你的组件从这里接入，无需读 Run 的其余部分。
//
// 写法：业务组件放 internal/ 下你自己的包（约定：Config / Default /
// New / Start / Stop，可选 ApplyConfig），在此用 AddComponent 接线。
// 模板内置的占位组件 internal/biz 演示了完整链路，项目落地后替换该包。
package app

import (
	"go_template/internal/biz"
	"go_template/internal/config"
)

// setupBiz 装配全部业务组件；任一组件读配置或构造失败 → 引导失败（退出码 1）。
func setupBiz(t *config.Tree, r *runner, meta Meta) error {
	// 占位业务：读 biz 节构造 Hello，启动时输出一句日志。
	// Hello 没有实现 ApplyConfig，所以改 biz 节不热更（重启生效）。
	if _, err := AddComponent(t, r, "biz", biz.Default(),
		func(c biz.Config) (*biz.Hello, error) { return biz.New(c) }); err != nil {
		return err
	}

	// 追加更多业务组件照此写。前一个组件的返回值可以直接传给下一个
	// 组件的构造参数，依赖方向即书写顺序：
	//
	// redis, err := AddComponent(t, r, "redis", redis.Default(), redis.New)
	// if err != nil {
	// 	return err
	// }
	// cache, err := AddComponent(t, r, "cache", cache.Default(),
	// 	func(c cache.Config) (*cache.Cache, error) {
	// 		return cache.New(c, cache.WithRedis(redis.Client()))
	// 	})
	// if err != nil {
	// 	return err
	// }
	_ = meta // 需要应用元数据的组件（如注册组件的 service name）从这里取
	return nil
}
