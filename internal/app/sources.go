package app

import "go_template/internal/config"

// setupSources 接入远程配置源（如 nacos）。模板内为空实现：
// 引入源资产时在此填写，不算修改模板。
//
// 填写示例（nacos 资产，客户端直接满足 Source 签名，无需适配代码）：
//
//	cfg, err := config.Decode(t, "nacos", nacos.Default())
//	if err != nil {
//		return err
//	}
//	cc, err := nacos.NewCfgClient(cfg)
//	if err != nil {
//		return err
//	}
//	return t.Attach(cc) // 等首份远程快照合并后才返回
func setupSources(t *config.Tree) error {
	return nil
}
