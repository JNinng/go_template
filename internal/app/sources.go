package app

import "go_template/internal/config"

// setupSources 接入远程配置源。模板内为空实现：引入源组件时在此填写，
// 不算修改模板。
//
// 内置组件 nacos（internal/components/nacos，依赖已在 go.mod）解注释即用：
//
//	import "go_template/internal/components/nacos"
//
//	func setupSources(t *config.Tree) error {
//		cfg, err := config.Decode(t, "nacos", nacos.Default())
//		if err != nil {
//			return err
//		}
//		cc, err := nacos.NewCfgClient(cfg)
//		if err != nil {
//			return err
//		}
//		return t.Attach(cc) // 等首份远程快照合并后才返回
//	}
//	// 并在 configs/config.yaml 追加 nacos 节（见组件 README）
//
// 其它源资产：go get 后按相同三步接入（解码 → 构造 → Attach）。
func setupSources(t *config.Tree) error {
	return nil
}
