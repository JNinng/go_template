// 业务组件的装配入口：你的组件从这里接入，无需读 Run 的其余部分。
//
// 写法：业务组件放 internal/ 下你自己的包（约定：Config / Default /
// New / Start / Stop，可选 ApplyConfig 与节名自述 SectionName/Section），
// 在此用 AddComponent 接线。
// 模板内置的占位组件 internal/biz 演示了完整链路，项目落地后替换该包。
package app

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"

	"go.uber.org/zap"

	"go.uber.org/zap/zapcore"

	"go_template/internal/biz"
	"go_template/internal/components/greeter"
	"go_template/internal/components/httpserver"
	"go_template/internal/components/otelc"
	"go_template/internal/components/promc"
	"go_template/internal/components/zapc"
	"go_template/internal/config"
	"go_template/internal/runner"
	"go_template/pkg/version"
)

// setupBiz 装配全部业务组件；任一组件读配置或构造失败 → 引导失败（退出码 1）。
// 书写顺序即依赖顺序：可观测三件套（otelc → zapc → promc）在前，
// httpserver 最后（它的中间件消费前两者装配的全局与 promc 的 handler）。
func setupBiz(t *config.Tree, r *runner.Runner, meta Meta) error {
	// 实例 ID 单一事实源：响应头 X-Instance-IDs、OTel resource
	// （service.instance.id）与启动日志共用同一值。
	instanceID := httpserver.InstanceID(meta.Name)

	// otelc：链路追踪 + 日志注入（trace_id/span_id/request_id）。
	// 资源标识：name/env 从元数据、version 从 pkg/version、instanceID 从上。
	tr, err := AddComponent(t, r, otelc.SectionName, otelc.Default(),
		func(c otelc.Config) (*otelc.Tracer, error) {
			return otelc.New(c,
				otelc.WithService(meta.Name, meta.Env, version.Version),
				otelc.WithInstanceID(instanceID))
		})
	if err != nil {
		return err
	}

	// zapc 接管 observ 默认日志后端（模板自持的 log: 节被遮蔽，删掉本段
	// 接线即回落 slog 链路）；配置非法或输出打不开 → 引导失败。
	// WithCore(tr.LogCore()) 无条件传参：otelc 未启用 OTLP 日志导出时
	// LogCore 为 nil，zapc.WithCore(nil) 被忽略，接线无需分支。
	// WithCtxAttrs(otelc.CtxLogAttrs) 把链路注入沉入 zaplog 适配层：
	// 走 observ 默认日志器的调用自动带 trace_id/span_id/request_id，
	// caller 定位不随封装漂移（须在 zapc 之前装配 otelc，即上文顺序）。
	_, err = AddComponent(t, r, zapc.SectionName, zapc.Default(),
		func(c zapc.Config) (*zapc.Log, error) {
			return zapc.New(c,
				zapc.WithCore(tr.LogCore()),
				zapc.WithCtxAttrs(otelc.CtxLogAttrs))
		})
	if err != nil {
		return err
	}

	// 请求日志：独立 zapc 实例（非组件，无生命周期启停、只有 sink 回收）。
	// 配置整体继承 zapc 节（level 热更跟随、format/轮转/控制台开关同源），
	// 仅两处派生：path 固定为 zapc.path 同目录的 req.log（path 为空时
	// 兜底 log/req.log——访问日志落盘是独立诉求，不随控制台形态缩水）；
	// caller 关闭（访问日志 caller 恒为中间件同一行，无定位价值）。
	// 热更经 config.Watch 派生转发（收敛判断在 zapc 侧照常生效）。
	// 停机钩子先于 httpserver 注册：逆序停止时 httpserver 排空完成后才
	// 刷盘关闭 sink，排空期的访问日志不丢。
	accessLog, err := bindRequestLogger(t, r)
	if err != nil {
		return err
	}

	// promc：指标与健康检查。addr 缺省空 = 不自起独立 server，
	// handler 由 httpserver 单端口收编（见下 WithProm）。
	pm, err := AddComponent(t, r, promc.SectionName, promc.Default(), promc.New)
	if err != nil {
		return err
	}

	// httpserver：业务 HTTP Server（中间件链、/livez /readyz /version
	// /debug/pprof，promc 的 /metrics /health 一并挂进业务端口）。
	// WithAccessLogger(accessLog) 传方法值（bindRequestLogger 返回的
	// Current）：每请求原子取当前实例，请求日志热更重建自动跟随。
	hs, err := AddComponent(t, r, httpserver.SectionName, httpserver.Default(),
		func(c httpserver.Config) (*httpserver.Server, error) {
			return httpserver.New(c,
				httpserver.WithService(meta.Name, meta.Env, version.Version),
				httpserver.WithProm(pm),
				httpserver.WithAccessLogger(accessLog))
		})
	if err != nil {
		return err
	}

	// greeter：周期问候的约定示范组件，这里另挂一条演示路由展示业务侧
	// 用法（业务路由一律在装配期注册——httpserver 的注册窗口在 Start 关闭）。
	g, err := AddComponent(t, r, greeter.SectionName, greeter.Default(),
		func(c greeter.Config) (*greeter.Greeter, error) { return greeter.New(c) })
	if err != nil {
		return err
	}
	hs.HandleFunc("/api/v1/greet", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"message":    g.Message(),
			"request_id": httpserver.RequestID(r.Context()), // 中间件注入，业务直接取
		})
	})

	// 占位业务：读 biz 节构造 Hello，启动时输出一句日志。
	// Hello 没有实现 ApplyConfig，所以改 biz 节不热更（重启生效）。
	if _, err := AddComponent(t, r, biz.SectionName, biz.Default(),
		func(c biz.Config) (*biz.Hello, error) { return biz.New(c) }); err != nil {
		return err
	}

	// 运行时需要拉取某节当前生效值（pull，Watch 的 push 之外的选项）：
	// 装配点绑定句柄，任意时刻 Get——与 Decode 同一路径、并发安全：
	// hsCfg := config.Bind(t, httpserver.SectionName, httpserver.Default())
	// cfg, err := hsCfg.Get()
	//
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
	//
	// 跨组件健康检查由装配点胶水登记（组件间零 import）：
	// pm.RegisterCheck("nacos", nc.Check)
	return nil
}

// bindRequestLogger 构造 httpserver 的请求日志记录器：独立 zapc 实例
// （配置继承 zapc 节、热更跟随、等级独立门控），path 派生为 req.log、
// caller 关闭。返回 getter（传 WithAccessLogger）——zapc.LoggerKit 的
// Current 方法值即热更安全的取用。sink 回收注册为停机钩子（须在
// httpserver 之前调用本函数，保证逆序停止时先停 server 再关 sink）。
func bindRequestLogger(t *config.Tree, r *runner.Runner) (func() *zap.Logger, error) {
	// 节句柄取初值（与 AddComponent 的解码同路径）；Watch 挂派生转发。
	sec := config.Bind(t, zapc.SectionName, zapc.Default())
	base, err := sec.Get()
	if err != nil {
		return nil, err
	}

	// 派生规则：path → 同目录 req.log（path 为空兜底 log/req.log）；
	// 其余字段（level/format/轮转/控制台）原样继承，热更整体跟随。
	derive := func(c zapc.Config) zapc.Config {
		dir := "log"
		if c.Path != "" {
			dir = filepath.Dir(c.Path)
		}
		c.Path = filepath.Join(dir, "req.log")
		return c
	}
	// caller 关闭是编码层定制（构建期属性，重建自动带上），不经派生：
	// 访问日志 caller 恒为中间件同一行，无定位价值。
	noCaller := func(e *zapcore.EncoderConfig) { e.CallerKey = "" }

	watch := func(apply func(zapc.Config) error) (cancel func()) {
		return config.Watch(t, zapc.SectionName, zapc.Default(),
			func(c zapc.Config) error { return apply(derive(c)) })
	}
	kit, err := zapc.NewLogger(derive(base), zapc.WithWatch(watch), zapc.WithEncoderConfig(noCaller))
	if err != nil {
		return nil, err
	}
	r.Add("req-log", nil, func(context.Context) error {
		_ = kit.Current().Sync()
		kit.Close()
		return nil
	})
	return kit.Current, nil
}
