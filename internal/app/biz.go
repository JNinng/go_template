// 业务组件的装配入口：你的组件从这里接入，无需读 Run 的其余部分。
//
// 写法：业务组件放 internal/ 下你自己的包（约定：Config / Default /
// New / Start / Stop，可选 ApplyConfig），在此用 AddComponent 接线。
// 模板内置的占位组件 internal/biz 演示了完整链路，项目落地后替换该包。
package app

import (
	"encoding/json"
	"net/http"

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
	tr, err := AddComponent(t, r, "otelc", otelc.Default(),
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
	_, err = AddComponent(t, r, "zapc", zapc.Default(),
		func(c zapc.Config) (*zapc.Log, error) { return zapc.New(c, zapc.WithCore(tr.LogCore())) })
	if err != nil {
		return err
	}

	// promc：指标与健康检查。addr 缺省空 = 不自起独立 server，
	// handler 由 httpserver 单端口收编（见下 WithProm）。
	pm, err := AddComponent(t, r, "promc", promc.Default(), promc.New)
	if err != nil {
		return err
	}

	// httpserver：业务 HTTP Server（中间件链、/livez /readyz /version
	// /debug/pprof，promc 的 /metrics /health 一并挂进业务端口）。
	hs, err := AddComponent(t, r, "httpserver", httpserver.Default(),
		func(c httpserver.Config) (*httpserver.Server, error) {
			return httpserver.New(c,
				httpserver.WithService(meta.Name, meta.Env, version.Version),
				httpserver.WithProm(pm))
		})
	if err != nil {
		return err
	}

	// greeter：周期问候的约定示范组件，这里另挂一条演示路由展示业务侧
	// 用法（业务路由一律在装配期注册——httpserver 的注册窗口在 Start 关闭）。
	g, err := AddComponent(t, r, "greeter", greeter.Default(),
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
	//
	// 跨组件健康检查由装配点胶水登记（组件间零 import）：
	// pm.RegisterCheck("nacos", nc.Check)
	return nil
}
