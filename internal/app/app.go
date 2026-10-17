// Package app 是装配层：启动时序、组件装配、优雅停机。
package app

import (
	"log/slog"

	"go_template/internal/config"

	"go_template/internal/runner"
)

// Run 是模板唯一的启动时序。顺序即依赖顺序：
// 默认静默 → 配置 → 远程源 → 日志 → 元数据 → 业务组件 → 运行。
// 任一步失败即引导失败：错误上抛，由命令层直写 stderr、退出码 1。
func Run(configPath, env, logLevel string) error {
	// 0. 默认静默：日志后端装配前，全局日志器为 discard（对齐"未配置
	//    零输出"的默认语义；stdlib 缺省 logger 会把启动期日志直写 stderr）。
	//    同时保证此后任何 slog.SetDefault 装饰（如 otelc 链路注入）包裹的
	//    都不是 stdlib 内部缺省 handler——其写路径经 stdlib log 桥，包裹后
	//    再接管会自环死锁（otelc.installLogTrace 的守卫依据）。
	slog.SetDefault(slog.New(slog.DiscardHandler))

	var overrides []config.Override
	if logLevel != "" {
		overrides = append(overrides, config.Override{Key: "log.level", Value: logLevel})
	}

	// 1. 加载配置：基础文件 + 多环境文件叠加，应用 --log-level 覆盖，
	//    并开始监听文件变更（热更的来源）。
	t, err := config.Load(configPath, env, overrides...)
	if err != nil {
		return err
	}

	// 2. 接入远程配置源（如 nacos）。必须在日志装配之前：
	//    log 节的初值才能包含远程下发的值。
	if err := setupSources(t); err != nil {
		return err
	}

	// 3. 装配日志后端：读 log 节，设 slog 默认后端，订阅 level 热更。
	r := runner.New()
	if err := setupLogging(t, r); err != nil {
		return err
	}

	// 4. 解析应用元数据，注册启动行组件（首个启动，输出 service_started）。
	meta, effEnv, err := loadMeta(t, env)
	if err != nil {
		return err
	}
	r.Add("announce", newAnnouncer(meta, effEnv).Start, nil)

	// 5. 装配业务组件——你的业务从这里接入（见 biz.go）。
	if err := setupBiz(t, r, meta); err != nil {
		return err
	}

	// 6. 运行：顺序启动全部组件 → 阻塞等待信号 → 逆序停止（预算内）。
	return r.Run()
}
