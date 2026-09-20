// Package nacos 是内置组件库的 nacos 双角色客户端（两个独立客户端）：
//
//   - CfgClient——配置中心客户端，Source 兼容签名（结构化类型满足，
//     直接传入 config.Tree.Attach）；
//   - Reg——服务注册客户端，标准生命周期签名（New / Start / Stop）。
//
// 两者分立配置（nacos.config / nacos.registrar 子节）、独立客户端、互不
// 共享连接。nacos 节为启动期配置：不实现 ApplyConfig，变更仅下次启动生效。
// 引导自身所需配置（连接参数）只能来自本地层——读它时远程尚未连通。
//
// 第三方依赖：github.com/nacos-group/nacos-sdk-go/v2 + github.com/jninng/observ
// （删除本组件目录并 go mod tidy 后即从 go.mod 清除）。
package nacos

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"

	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"

	"github.com/jninng/observ"
)

// ConfigCenter 为 nacos.config 子节（配置中心角色）。
type ConfigCenter struct {
	Enabled     bool   `yaml:"enabled"`     // 角色开关，缺省 true
	Unreachable string `yaml:"unreachable"` // 不可达策略：fail（缺省，启动报错）| disable（告警后纯本地继续，热更停摆）
	Addr        string `yaml:"addr"`        // 服务地址 host:port（gRPC 端口由 SDK 按偏移自动推导）
	Namespace   string `yaml:"namespace"`   // 命名空间 ID
	Group       string `yaml:"group"`       // 配置分组
	DataID      string `yaml:"data_id"`     // 订阅的配置集标识（全量快照来源）
	Username    string `yaml:"username"`    // 凭据（与 registrar 分立，两者常为不同集群）
	Password    string `yaml:"password"`    // 凭据
}

// ServiceRegistry 为 nacos.registrar 子节（服务注册角色）。
// 实例标识（service name / port）不经配置，由装配点以参数显式传入
// （service name 传模板元数据 meta.Name）。
type ServiceRegistry struct {
	Enabled     bool   `yaml:"enabled"`     // 角色开关，缺省 false——注册需显式提供实例端口，缺省开启只会保证失败
	Unreachable string `yaml:"unreachable"` // 不可达策略：fail（缺省）| disable（告警后旁路运行，暂不被发现）
	Addr        string `yaml:"addr"`        // 服务地址 host:port
	Namespace   string `yaml:"namespace"`   // 命名空间 ID
	Group       string `yaml:"group"`       // 注册分组
	ServiceIP   string `yaml:"service_ip"`  // 注册用本机 IP，缺省自动探测出口 IP（UDP 拨号不实际发包）
	Username    string `yaml:"username"`    // 凭据
	Password    string `yaml:"password"`    // 凭据
}

// Config 为 nacos 节整体（两个子节分立、独立解析）。
type Config struct {
	Config    ConfigCenter    `yaml:"config"`    // 配置中心子节
	Registrar ServiceRegistry `yaml:"registrar"` // 服务注册子节
}

// Default 返回 nacos 节默认值基座（初始解码与重解码共用；本组件不热更，
// 仅初始解码）。配置中心缺省启用 + fail；注册缺省禁用（启用需实例端口）。
func Default() Config {
	return Config{
		Config: ConfigCenter{
			Enabled:     true,
			Unreachable: "fail",
			Addr:        "127.0.0.1:8848",
			Group:       "DEFAULT_GROUP",
			DataID:      "app.yaml",
		},
		Registrar: ServiceRegistry{
			Enabled:     false,
			Unreachable: "fail",
			Addr:        "127.0.0.1:8848",
			Group:       "DEFAULT_GROUP",
		},
	}
}

// Option 是两个客户端共用的构造选项。
type Option func(*options)

// WithLogger 显式注入日志面（保留给测试捕获）。缺省不注入——调用点
// 动态读 observ.DefaultLogger()：本组件可能在 setupSources 构造（早于
// 日志装配），构造期快照会把告警永久固定在 Noop。
func WithLogger(l observ.Logger) Option {
	return func(o *options) { o.logger = l }
}

type options struct {
	logger observ.Logger // 显式注入的日志面；nil 表示动态读默认
}

// newOptions 应用全部选项。
func newOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// logWarn 记策略降级 / 数据异常类告警（Warn：显式决策或旁路角色，无需即时告警）。
// SDK 回调无业务 ctx，恒 Background（nacos 生命周期事件不在业务 span 内）。
func (o options) logWarn(msg string, attrs ...slog.Attr) {
	o.current().Log(context.Background(), slog.LevelWarn, msg, attrs...)
}

// logInfo 记生命周期关键节点（Info）。
func (o options) logInfo(msg string, attrs ...slog.Attr) {
	o.current().Log(context.Background(), slog.LevelInfo, msg, attrs...)
}

// current 返回生效的日志面：显式注入优先，否则动态读默认（换后端立即生效）。
func (o options) current() observ.Logger {
	if o.logger != nil {
		return o.logger
	}
	return observ.DefaultLogger()
}

// logPolicyWarn 记不可达策略降级告警：observ 动态读 + 直写 stderr 双通道。
// 降级事件发生在引导窗口（setupSources 早于日志装配）时 observ 可能仍是
// Noop，stderr 保证高信号运维事实永不丢失；日志就绪后最多重复一行，可接受。
func (o options) logPolicyWarn(msg string, attrs ...slog.Attr) {
	o.current().Log(context.Background(), slog.LevelWarn, msg, attrs...)
	fmt.Fprintf(os.Stderr, "[WARN] nacos: %s %v\n", msg, attrs)
}

// validatePolicy 校验 unreachable 取值（fail | disable）。
func validatePolicy(policy string) error {
	switch policy {
	case "fail", "disable":
		return nil
	}
	return fmt.Errorf("nacos: unreachable must be %q or %q, got %q", "fail", "disable", policy)
}

// parseAddr 严格解析 host:port——畸形地址（如 "8848abc"、端口越界）在构造期
// 拒绝，不得静默变成合法参数、把错误延迟到连接期。
func parseAddr(addr string) (host string, port uint64, err error) {
	h, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("nacos: invalid addr %q: %w", addr, err)
	}
	p, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil || p < 1 {
		return "", 0, fmt.Errorf("nacos: invalid port %q in addr %q (want 1-65535)", portStr, addr)
	}
	return h, p, nil
}

// newClientParam 构造 SDK 客户端参数（两个角色各自构造、各持实例，不共享连接）。
func newClientParam(addr, namespace, username, password string) (vo.NacosClientParam, error) {
	host, port, err := parseAddr(addr)
	if err != nil {
		return vo.NacosClientParam{}, err
	}
	return vo.NacosClientParam{
		ClientConfig: &constant.ClientConfig{
			NamespaceId:         namespace,
			Username:            username,
			Password:            password,
			NotLoadCacheAtStart: true,
		},
		ServerConfigs: []constant.ServerConfig{
			*constant.NewServerConfig(host, port),
		},
	}, nil
}

// localIP 探测本机出口 IP（UDP 拨号不实际发包）。target 优先用目标服务
// 地址：探得的正是"通往该服务的源 IP"，且不依赖外部路由（无外网环境
// 同样可探测）；target 不可解析时退回公共兜底地址。
func localIP(target string) (string, error) {
	if _, _, err := net.SplitHostPort(target); err != nil {
		target = "8.8.8.8:80"
	}
	conn, err := net.Dial("udp", target)
	if err != nil {
		return "", fmt.Errorf("nacos: detect local ip (via %s): %w", target, err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
}
