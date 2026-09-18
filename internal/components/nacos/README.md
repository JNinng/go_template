# nacos — 内置组件（配置中心 + 服务注册）

nacos 双角色客户端，两角色分立配置、独立客户端、互不共享连接：

- **CfgClient**——配置中心客户端，Source 兼容签名（结构化类型满足，直接传入 `config.Tree.Attach`）
- **Reg**——服务注册客户端，标准生命周期签名（`New` / `Start` / `Stop`）

实例标识（service name / port）由装配点显式传入（service name 传模板元数据 `meta.Name`）。

**第三方依赖**：`github.com/nacos-group/nacos-sdk-go/v2` + `github.com/jninng/observ`。
删除本目录并 `go mod tidy` 后即从 go.mod 清除。

## 接入（复制即用，无需 go get——组件已在本模块内）

**源触点** `internal/app/sources.go`：

```go
func setupSources(t *config.Tree) error {
	cfg, err := config.Decode(t, "nacos", nacos.Default())
	if err != nil {
		return err
	}
	cc, err := nacos.NewCfgClient(cfg)
	if err != nil {
		return err // unreachable=fail 在此报错
	}
	return t.Attach(cc) // 等首份远程快照合并后才返回
}
```

**组件触点** `internal/app/biz.go`（注册中心，serviceName 传 `meta.Name`）：

```go
cfg, err := config.Decode(t, "nacos", nacos.Default())
if err != nil {
	return err
}
reg, err := nacos.NewReg(cfg, meta.Name, 8080)
if err != nil {
	return err
}
r.Add("nacos-reg", reg.Start, reg.Stop)
```

**配置节**（追加到 `configs/config.yaml`，或由 `config.Dump(os.Stdout, "nacos", nacos.Default())` 生成）：

```yaml
nacos:
  config:
    enabled: true          # 改 false 即禁用（空快照、不触网）
    unreachable: fail      # fail=启动报错 | disable=告警后纯本地配置继续
    addr: "127.0.0.1:8848"
    namespace: ""
    group: DEFAULT_GROUP
    data_id: app.yaml
    username: ""
    password: ""
  registrar:
    enabled: false         # 启用需在装配点提供 service name 与 port
    unreachable: fail      # fail=启动报错 | disable=告警后旁路运行（暂不被发现）
    addr: "127.0.0.1:8848"
    namespace: ""
    group: DEFAULT_GROUP
    service_ip: ""         # 缺省自动探测本机出口 IP
    username: ""
    password: ""
```

## 行为契约

- **全量快照**：每次变更推送整份 dataId 内容的解析结果（yaml）；空内容 = 空快照（删除语义）；解析失败告警丢弃、维持上一有效快照
- **unreachable 策略**：`fail`（缺省）→ 引导失败退出码 1；`disable` → observ + stderr 双通道告警后降级（config：纯本地配置、热更停摆；registrar：旁路运行、暂不被发现）。降级告警走双通道是因为源触点早于日志装配，单靠 observ 会被 Noop 吞掉
- **本组件全部字段不热更**：连接参数无法热切换，不实现 `ApplyConfig`，变更仅下次启动生效
- **地址严格校验**：畸形 addr（非法端口等）无论策略如何都构造期 fail-fast
- **本地探测**：`service_ip` 缺省时向服务中心地址做 UDP 拨号探测出口 IP（不实际发包）

## 字段速查

| 节 | 字段 | 缺省 | 说明 |
|---|---|---|---|
| config | enabled | true | 角色开关；false 时推送空快照 |
| config | unreachable | fail | fail / disable |
| config | addr / namespace / group / data_id | 127.0.0.1:8848 / "" / DEFAULT_GROUP / app.yaml | 连接与定位 |
| config | username / password | "" | 凭据（与 registrar 分立，常为不同集群） |
| registrar | enabled | false | 启用需实例标识（装配点传参） |
| registrar | unreachable | fail | fail / disable |
| registrar | addr / namespace / group | 127.0.0.1:8848 / "" / DEFAULT_GROUP | 连接与分组 |
| registrar | service_ip | 自动探测 | 注册用本机 IP |
| registrar | username / password | "" | 凭据 |

## 生命周期 API

| API | 说明 |
|---|---|
| `Default() Config` | 默认值基座（与 `config.Decode` 成对使用） |
| `NewCfgClient(cfg Config, opts ...Option) (*CfgClient, error)` | 构造即校验，不连接；失败无资源需清理 |
| `(*CfgClient).Name() string` | 源名称 "nacos" |
| `(*CfgClient).Start(ctx, push func(map[string]any)) error` | Source 签名：连接 + 首份全量快照 + 订阅；阻塞至 ctx 取消 |
| `NewReg(cfg Config, serviceName string, port int, opts ...Option) (*Reg, error)` | 构造即校验；enabled=false 返回旁路实例 |
| `(*Reg).Start(ctx) error` | 注册实例（Ephemeral，心跳由 SDK 维持） |
| `(*Reg).Stop(ctx) error` | 注销 + 释放连接；幂等可重入 |
| `WithLogger(observ.Logger) Option` | 显式注入日志面（测试捕获用；缺省动态读默认） |
