// Package zapc 是内置组件库的 zap 日志组件：生命周期装配 + 配置热更
// （级别即时生效，其余变更重建实例并同步 zap 全局）。文件输出带轮转
// （大小 / 天数 / 份数 / 压缩），控制台输出固定人类可读格式。接线即接管
// observ 默认日志器（模板自持的 log: 节被遮蔽），配置节名以 SectionName
// 常量自述（"zapc"，与包名一致）。
//
// 文件布局：zap.go 组件壳（生命周期 + observ 桥）；config.go 配置节；
// kit.go 工厂、热更状态机与调用面；build.go sink 与编码组装。
//
// 第三方依赖：go.uber.org/zap + gopkg.in/natefinch/lumberjack.v2（轮转）+
// github.com/jninng/observ/adapters/zaplog（observ 桥）
// （删除本目录并 go mod tidy 后即从 go.mod 清除）。
package zapc

import (
	"context"
	"log/slog"

	"github.com/jninng/observ"
	"github.com/jninng/observ/adapters/zaplog"
	"go.uber.org/zap"
)

// Log 是 zap 全局日志的组件壳：Start 装全局、Stop 刷盘收尾、ApplyConfig
// 热更。组件本身不设调用面——日志输出走 zap 全局（zap.L / zap.S）或
// kit 的 Info / Check。
type Log struct {
	kit LoggerKit
}

// New 构造即校验并构建日志实例（打开 sink），并接管 observ 默认日志器
// （模板自持的 log: 节由此被遮蔽）；opts 透传 kit（如 WithCore 并入
// OTLP 日志导出 core、WithCtxAttrs 并入链路注入）。失败即未启动，已开
// 句柄就地关闭，无需调用方清理。
//
// 接管形态是稳定桥（zaplog.NewDynamic 包装 kit.CurrentSkip1）：桥身份
// 恒定，热更重建只换 kit 内实例、桥自动跟随——SetDefaultLogger 在进程
// 内一次性完成，接管不感知既有默认日志器的形态。caller skip 在实例侧烘焙
// （curSkip1 = AddCallerSkip(1)，恒补偿 zaplog 适配帧），调用面无装饰
// 层，定位不随封装漂移。链路注入由 WithCtxAttrs 传入提取函数（装配点
// 传 otelc.CtxLogAttrs），在适配层内部完成——本组件不 import otel。
// 未传 WithCtxAttrs 时接管会整体替换此前安装的 observ 默认（含装饰），
// 链路注入须由装配点显式传入，组件间不互相探测。
func New(cfg Config, opts ...Option) (*Log, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	kit, err := NewLogger(cfg, opts...)
	if err != nil {
		return nil, err
	}
	observ.SetDefaultLogger(zaplog.NewDynamic(kit.CurrentSkip1, zaplog.WithCtxAttrs(o.ctxAttrs)))
	return &Log{kit: kit}, nil
}

// Start 把当前实例安装为 zap 全局（zap.L / zap.S）。
// Section 返回本组件的配置节名（统一节名获取接口，恒返回 SectionName）。
func (z *Log) Section() string { return SectionName }

func (z *Log) Start(ctx context.Context) error {
	zap.ReplaceGlobals(z.kit.Current())
	observ.DefaultLogger().Log(ctx, slog.LevelInfo, "zapc_started")
	return nil
}

// Stop 刷盘并释放 sink 句柄；幂等，错误均吞（stderr Sync 在个别平台报
// EINVAL 噪声，不视作停机失败）。
func (z *Log) Stop(ctx context.Context) error {
	observ.DefaultLogger().Log(ctx, slog.LevelInfo, "zapc_stopping")
	_ = z.kit.Current().Sync()
	z.kit.Close()
	return nil
}

// ApplyConfig 热更入口，委托 kit.Apply；AddComponent 据此自动订阅配置节。
// 可能在 Start 之前被调用（订阅建立时的收敛首调）。
func (z *Log) ApplyConfig(cfg Config) error {
	return z.kit.Apply(cfg)
}
