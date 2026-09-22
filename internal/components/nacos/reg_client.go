package nacos

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

// Reg 是服务注册角色：标准生命周期签名（Start 注册 + SDK 维持心跳，
// Stop 注销并释放连接）。实例标识（serviceName / port）由装配点显式
// 传入——serviceName 即模板元数据 meta.Name。
type Reg struct {
	cfg         ServiceRegistry // 构造期校验过的连接配置（启动期配置，不热更）
	serviceName string          // 注册的服务名（装配点传 meta.Name）
	port        int             // 注册的服务端口（启用时必填 > 0）
	opt         options         // 日志面等选项

	ip      string                      // 注册用的本机 IP（Start 时解析）
	client  naming_client.INamingClient // SDK 客户端，注册成功后非 nil
	started bool                        // 注册成功标记（Stop 判定是否需注销）
}

// NewReg 构造注册客户端：构造即校验（策略取值、地址格式、启用时端口必填），
// 无副作用、不连接。enabled=false 时返回旁路实例（Start 空操作），装配点
// 可无条件注册其生命周期。
func NewReg(cfg Config, serviceName string, port int, opts ...Option) (*Reg, error) {
	c := cfg.Registrar
	if err := validatePolicy(c.Unreachable); err != nil {
		return nil, err
	}
	if _, _, err := parseAddr(c.Addr); err != nil {
		return nil, err // 本地配置畸形不是"不可达"事实：无论策略如何都 fail-fast
	}
	if c.Enabled {
		if serviceName == "" {
			return nil, fmt.Errorf("nacos registrar: service_name is required when registrar.enabled is true")
		}
		if port <= 0 || port > 65535 {
			return nil, fmt.Errorf("nacos registrar: port must be 1-65535 when registrar.enabled is true, got %d", port)
		}
	}
	return &Reg{cfg: c, serviceName: serviceName, port: port, opt: newOptions(opts)}, nil
}

// Start 连接服务中心并注册实例（Ephemeral：心跳由 SDK 维持）。
// 不可达归化（unreachable 策略）：fail（缺省）→ 返回 error，装配点 fail-fast；
// disable → 记显著告警、旁路降级（服务功能完整，只是暂不被发现），返回 nil。
// 注册窗口内的一切错误——拨号不通、鉴权失败、参数被服务端拒绝——在
// disable 下一律旁路降级；已知代价是鉴权配置错误时静默不注册、仅一条
// Warn，靠告警巡检兜住。
func (r *Reg) Start(ctx context.Context) error {
	if !r.cfg.Enabled {
		r.opt.current().Log(context.Background(), slog.LevelInfo, "nacos_registrar_disabled")
		return nil
	}

	ip := r.cfg.ServiceIP
	if ip == "" {
		// 探测目标即服务中心地址；失败同样归入不可达策略（disable 时旁路
		// 降级）——本地探测失败不得绕过显式声明
		var err error
		ip, err = localIP(r.cfg.Addr)
		if err != nil {
			return r.failOrSkip(err)
		}
	}
	r.ip = ip

	param, err := newClientParam(r.cfg.Addr, r.cfg.Namespace, r.cfg.Username, r.cfg.Password)
	if err != nil {
		return err // 构造期已校验过地址，此处不可达
	}
	client, err := clients.NewNamingClient(param)
	if err != nil {
		return r.failOrSkip(err)
	}
	ok, err := client.RegisterInstance(vo.RegisterInstanceParam{
		Ip:          ip,
		Port:        uint64(r.port),
		Weight:      1,
		Enable:      true,
		Healthy:     true,
		Ephemeral:   true,
		ServiceName: r.serviceName,
		GroupName:   r.cfg.Group,
	})
	if err != nil || !ok {
		client.CloseClient()
		if err == nil {
			err = fmt.Errorf("nacos registrar: register instance returned false")
		}
		return r.failOrSkip(err)
	}

	r.client = client
	r.started = true
	r.opt.current().Log(context.Background(), slog.LevelInfo, "nacos_register_success",
		slog.String("service_name", r.serviceName),
		slog.String("addr", r.cfg.Addr),
		slog.String("service_ip", ip),
		slog.Int("service_port", r.port))
	return nil
}

// Stop 注销实例并释放连接（幂等：未注册或已停即空操作）。
func (r *Reg) Stop(ctx context.Context) error {
	if r.client == nil {
		return nil
	}
	if r.started {
		if _, err := r.client.DeregisterInstance(vo.DeregisterInstanceParam{
			Ip:          r.ip,
			Port:        uint64(r.port),
			ServiceName: r.serviceName,
			GroupName:   r.cfg.Group,
			Ephemeral:   true,
		}); err != nil {
			// 旁路清理失败：告警即可，不阻断停机流程
			r.opt.current().Log(context.Background(), slog.LevelWarn, "nacos_deregister_failed",
				slog.String("service_name", r.serviceName), slog.Any("error", err))
		}
	}
	r.client.CloseClient()
	r.client = nil
	r.started = false
	return nil
}

// failOrSkip 按策略归化注册期错误。
func (r *Reg) failOrSkip(err error) error {
	if r.cfg.Unreachable == "disable" {
		attrs := []slog.Attr{slog.String("addr", r.cfg.Addr), slog.Any("error", err)}
		r.opt.current().Log(context.Background(), slog.LevelWarn, "nacos_registrar_unreachable_disabled", attrs...)
		stderrWarn("nacos_registrar_unreachable_disabled", attrs...)
		return nil // 旁路降级：显式声明，进程继续运行
	}
	return fmt.Errorf("nacos registrar unreachable (addr %s): %w", r.cfg.Addr, err)
}
