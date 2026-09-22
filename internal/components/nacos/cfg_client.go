package nacos

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"gopkg.in/yaml.v3"
)

// sourceLike 声明 Source 兼容签名（结构化类型满足）：模板侧 config.Source
// 与此签名一致，CfgClient 零 import 模板即可直传 Attach。
type sourceLike interface {
	Name() string
	Start(ctx context.Context, push func(map[string]any)) error
}

var _ sourceLike = (*CfgClient)(nil)

// CfgClient 是配置中心角色：全量快照语义——每次配置变更以整份 dataId
// 内容解析后的 map 调用 push（非增量）；快照中消失的节即视为删除。
type CfgClient struct {
	cfg ConfigCenter // 构造期校验过的配置（启动期配置，不热更）
	opt options      // 日志面等选项

	mu     sync.Mutex                  // 保护 client 与 listenParam（Start/关闭互斥）
	client config_client.IConfigClient // SDK 客户端，Start 成功后非 nil
	listen vo.ConfigParam              // 监听参数（取消监听时复用）
}

// NewCfgClient 构造配置中心客户端：构造即校验（策略取值、地址格式），
// 无副作用、不连接——连接与订阅发生在 Start（失败即未启动，无资源需清理）。
// 返回 error 即装配点 fail-fast。
func NewCfgClient(cfg Config, opts ...Option) (*CfgClient, error) {
	c := cfg.Config
	if err := validatePolicy(c.Unreachable); err != nil {
		return nil, err
	}
	if _, _, err := parseAddr(c.Addr); err != nil {
		return nil, err // 本地配置畸形不是"不可达"事实：无论策略如何都 fail-fast
	}
	return &CfgClient{cfg: c, opt: newOptions(opts)}, nil
}

// Name 返回源名称（日志与错误归因）。
func (c *CfgClient) Name() string { return "nacos" }

// Start 连接配置中心、拉取初始全量内容并订阅变更；阻塞直至 ctx 取消。
// 首份快照经 push 合并完成后返回语义由模板侧 Attach 保证（同步等待）。
//
// 不可达与初始内容解析失败的归化（unreachable 策略）：
//   - fail（缺省）→ 返回 error，装配点 fail-fast；
//   - disable → 记显著告警、push 空快照（等效禁用，进程以纯本地配置运行），
//     其后不再推送（热更停摆），阻塞至 ctx 取消。
//
// 运行期（首快照之后）的变更解析失败：记告警丢弃本次、维持上一有效快照
// （全量快照语义下最终一致）。
func (c *CfgClient) Start(ctx context.Context, push func(map[string]any)) error {
	if !c.cfg.Enabled {
		c.opt.current().Log(context.Background(), slog.LevelInfo, "nacos_config_disabled")
		push(map[string]any{})
		<-ctx.Done()
		return nil
	}

	snap, client, listen, err := c.connect(push)
	if err != nil {
		if c.cfg.Unreachable == "disable" {
			// 显式降级：双通道告警后以纯本地配置继续（空快照解锁 Attach），
			// 热更停摆——阻塞至取消，绝不把降级当失败上抛
			attrs := []slog.Attr{slog.String("addr", c.cfg.Addr), slog.Any("error", err)}
			c.opt.current().Log(context.Background(), slog.LevelWarn, "nacos_config_unreachable_disabled", attrs...)
			stderrWarn("nacos_config_unreachable_disabled", attrs...)
			push(map[string]any{})
			<-ctx.Done()
			return nil
		}
		return fmt.Errorf("nacos config center unreachable (addr %s): %w", c.cfg.Addr, err)
	}

	c.mu.Lock()
	c.client, c.listen = client, listen
	c.mu.Unlock()

	push(snap)
	<-ctx.Done()

	c.mu.Lock()
	cl, ln := c.client, c.listen
	c.client, c.listen = nil, vo.ConfigParam{}
	c.mu.Unlock()
	if cl != nil {
		if ln.DataId != "" {
			_ = cl.CancelListenConfig(ln)
		}
		cl.CloseClient()
	}
	return nil
}

// connect 建立连接、订阅变更并拉取初始内容；任一步失败即回收半启动资源。
func (c *CfgClient) connect(push func(map[string]any)) (map[string]any, config_client.IConfigClient, vo.ConfigParam, error) {
	param, err := newClientParam(c.cfg.Addr, c.cfg.Namespace, c.cfg.Username, c.cfg.Password)
	if err != nil {
		return nil, nil, vo.ConfigParam{}, err
	}
	client, err := clients.NewConfigClient(param)
	if err != nil {
		return nil, nil, vo.ConfigParam{}, err
	}

	listen := vo.ConfigParam{
		DataId: c.cfg.DataID,
		Group:  c.cfg.Group,
		OnChange: func(namespace, group, dataId, data string) {
			snap, err := parseContent(data)
			if err != nil {
				// 数据异常（非技术故障）：告警丢弃，维持上一有效快照
				c.opt.current().Log(context.Background(), slog.LevelWarn, "nacos_config_parse_failed",
					slog.String("data_id", dataId), slog.Any("error", err))
				return
			}
			push(snap) // 捕获自 Start 的 push
		},
	}
	if err := client.ListenConfig(listen); err != nil {
		client.CloseClient()
		return nil, nil, vo.ConfigParam{}, err
	}

	content, err := client.GetConfig(vo.ConfigParam{DataId: c.cfg.DataID, Group: c.cfg.Group})
	if err != nil {
		client.CloseClient()
		return nil, nil, vo.ConfigParam{}, err
	}
	snap, err := parseContent(content)
	if err != nil {
		client.CloseClient()
		return nil, nil, vo.ConfigParam{}, fmt.Errorf("nacos: initial data_id content: %w", err)
	}
	return snap, client, listen, nil
}

// parseContent 把 dataId 全量内容解析为快照 map；空内容视为空快照（删除语义）。
func parseContent(content string) (map[string]any, error) {
	m := map[string]any{}
	if content == "" {
		return m, nil
	}
	if err := yaml.Unmarshal([]byte(content), &m); err != nil {
		return nil, fmt.Errorf("nacos: parse data_id content: %w", err)
	}
	return m, nil
}
