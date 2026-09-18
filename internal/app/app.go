// Package app 是装配层：启动时序、组件接线、优雅停机。
// 时序（docs/DESIGN.md §4）：config.Load → wireSource → setupLogging →
// meta → wire（业务入口 biz.go）→ runner.Run（启动行由 announce 组件
// 首个启动时输出）。
package app

import "go_template/internal/config"

// Run 是模板唯一的启动时序。任一步失败即引导失败：错误上抛，
// 由命令层直写 stderr、以退出码 1 终止（此时日志可能未就绪）。
// 时序纪律——先源后一切：远程源在 wireSource 接入，先于日志装配，
// log 节与元数据的初值因此包含完整"本地 + 远程 + 静态层"合并结果。
func Run(configPath, env, logLevel string) error {
	var overrides []config.Override
	if logLevel != "" {
		overrides = append(overrides, config.Override{Key: "log.level", Value: logLevel})
	}

	t, err := config.Load(configPath, env, overrides...)
	if err != nil {
		return err
	}
	if err := wireSource(t); err != nil {
		return err
	}

	r := new(runner)
	if err := setupLogging(t, r); err != nil {
		return err
	}

	meta, effEnv, err := loadMeta(t, env)
	if err != nil {
		return err
	}
	// announce 首个注册 → 首个启动（逆序停止时其 Stop 为 nil，运行器跳过）
	r.Add("announce", newAnnouncer(meta, effEnv).Start, nil)

	if err := wire(t, r, meta); err != nil {
		return err
	}
	return r.Run()
}
