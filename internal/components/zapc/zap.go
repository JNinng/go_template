// Package zapc 是内置组件库的 zap 日志组件：生命周期装配 + 配置热更
// （级别即时生效，其余变更重建实例并同步 zap 全局）。文件输出带轮转
// （大小 / 天数 / 份数 / 压缩），控制台输出固定人类可读格式。与模板自持
// 的 log 节（slog 链路）互不干涉，配置节名建议用 "zapc"。
//
// 文件布局：zap.go 组件壳（生命周期）；config.go 配置节；kit.go 工厂、
// 热更状态机与调用面；build.go sink 与编码组装。
//
// 第三方依赖：go.uber.org/zap + gopkg.in/natefinch/lumberjack.v2（轮转）
// （删除本目录并 go mod tidy 后即从 go.mod 清除）。
package zapc

import (
	"context"

	"go.uber.org/zap"
)

// Log 是 zap 全局日志的组件壳：Start 装全局、Stop 刷盘收尾、ApplyConfig
// 热更。组件本身不设调用面——日志输出走 zap 全局（zap.L / zap.S）或
// kit 的 Info / Check。
type Log struct {
	kit LoggerKit
}

// New 构造即校验并构建日志实例（打开 sink）；失败即未启动，已开句柄就地
// 关闭，无需调用方清理。
func New(cfg Config) (*Log, error) {
	kit, err := NewLogger(cfg)
	if err != nil {
		return nil, err
	}
	return &Log{kit: kit}, nil
}

// Start 把当前实例安装为 zap 全局（zap.L / zap.S）。
func (z *Log) Start(_ context.Context) error {
	zap.ReplaceGlobals(z.kit.Current())
	return nil
}

// Stop 刷盘并释放 sink 句柄；幂等，错误均吞（stderr Sync 在个别平台报
// EINVAL 噪声，不视作停机失败）。
func (z *Log) Stop(_ context.Context) error {
	_ = z.kit.Current().Sync()
	z.kit.Close()
	return nil
}

// ApplyConfig 热更入口，委托 kit.Apply；AddComponent 据此自动订阅配置节。
// 可能在 Start 之前被调用（订阅建立时的收敛首调）。
func (z *Log) ApplyConfig(cfg Config) error {
	return z.kit.Apply(cfg)
}
