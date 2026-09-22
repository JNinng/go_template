// Package zapc 是内置组件库的 zap 日志组件：生命周期装配 + 配置热更
// （级别即时生效，其余变更重建实例并同步 zap 全局）。文件输出带轮转
// （大小 / 天数 / 份数 / 压缩），控制台输出固定人类可读格式。接线即接管
// observ 默认日志器（模板自持的 log: 节被遮蔽），配置节名建议用 "zapc"。
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
// （zaplog 桥经 WithOnSwap 跟随热更重建自动重绑，模板自持的 log: 节由此
// 被遮蔽）；opts 透传 kit（如 WithCore 并入 OTLP 日志导出 core）。注意：
// 接管依赖内置的 WithOnSwap 回调，opts 再传自定义 WithOnSwap 会将其顶掉
// （Option 后者胜）、接管即失效。失败即未启动，已开句柄就地关闭，无需
// 调用方清理。
func New(cfg Config, opts ...Option) (*Log, error) {
	kit, err := NewLogger(cfg, append([]Option{
		WithOnSwap(func(l *zap.Logger) {
			bridge := zaplog.New(l.WithOptions(zap.AddCallerSkip(1)))
			// 接管尊重已装装饰器：默认日志器实现 Rebind 协议（如 otelc
			// 的链路注入装饰）时原地重绑后端，装饰在接管与热更重建后
			// 保持有效；未实现者维持整体替换
			if cur, ok := observ.DefaultLogger().(interface{ Rebind(observ.Logger) }); ok {
				cur.Rebind(bridge)
				return
			}
			observ.SetDefaultLogger(bridge)
		}),
	}, opts...)...)
	if err != nil {
		return nil, err
	}
	return &Log{kit: kit}, nil
}

// Start 把当前实例安装为 zap 全局（zap.L / zap.S）。
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
