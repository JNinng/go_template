package app

import (
	"go_template/internal/config"

	"github.com/jninng/nacos"
)

// setupSources 接入远程配置源。本分支对接真实 nacos（demo.yaml 全量快照）；
// 引导自身所需配置（连接参数）只来自本地文件——读它时远程尚未连通。
// Source 签名由结构化类型满足：nacos 客户端零适配直传 Attach。
func setupSources(t *config.Tree) error {
	cfg, err := config.Decode(t, "nacos", nacos.Default())
	if err != nil {
		return err
	}
	cc, err := nacos.NewCfgClient(cfg)
	if err != nil {
		return err // unreachable=fail 在此报错
	}
	return t.Attach(cc) // 等首份远程快照合并后才返回
}
