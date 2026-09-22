# httpserver — 内置组件（业务 HTTP Server）

标准库 `ServeMux` 路由（Go 1.22+ 方法与通配符模式）+ 预置可观测中间件链
（Recovery → CORS → 访问日志/指标 → otelhttp tracing → RequestID →
请求体上限），强绑定模板可观测设施：

- **日志**：访问日志直调 `zap.L()`（zapc 接管的全局）；低频生命周期日志
  走 observ（`httpserver_server_started` / `_stopping` / `_stopped`）
- **tracing**：otelhttp 经 otel 全局 TracerProvider（otelc 装配）；可信
  来源继承 `traceparent` 为父 span，不可信来源新建 root span 并把外部
  traceparent 转为 **Link**（防伪造污染拓扑，排查仍可回溯）；span 名
  在路由匹配后回填为 `GET /api/{id}` 并补 `http.route` 属性
- **指标**：`httpserver_requests_total`（method/pattern/status_class）
  与 `httpserver_request_duration_seconds`（method/pattern）注册到
  promc 私有 registry——`observ.Meter` 契约无 label 维度，基础设施
  直连 prometheus 原生 API（ADR-0005）
- **单端口收编**：promc 的 `/metrics` `/health` 经 `WithProm` 挂进业务
  路由（promc.addr 缺省空 = 不自起独立 server），readiness 复用其健康
  检查聚合

**第三方依赖**：`go.opentelemetry.io/contrib`（otelhttp）+
`go.opentelemetry.io/otel` + `github.com/rs/cors` +
`github.com/prometheus/client_golang` + `go.uber.org/zap`（zapc 已携带）
+ `github.com/jninng/observ` + `gopkg.in/yaml.v3`。
删除本目录并 `go mod tidy` 后即从 go.mod 清除。
依赖 promc 的接缝是 `PromProvider` 接口（结构化类型），本包不 import
promc——组件间零依赖约定不破。

**包结构**：公共契约（Config / New / Start / Stop / Handle / 选项 /
助手）全部由根包出口；实现细节在 `internal/` 子包（Go internal 机制
保证只被本组件引用）：

| 子包 | 职责 |
|---|---|
| `internal/middleware` | 链组装（Chain + Deps）与六个中间件、请求状态/响应记录器 |
| `internal/trust` | 可信代理网段表（trusted_proxies 的事实类型）与客户端 IP 剥离 |
| `internal/metric` | 预置 prometheus 指标（requests_total / duration_seconds） |
| `internal/endpoint` | 内置端点（探活/就绪/版本/pprof） |
| `internal/instance` | 实例 ID 解析（InstanceID 的实现） |

## 接入（复制即用，无需 go get——组件已在本模块内）

**组件触点** `internal/app/biz.go`（顺序即依赖：otelc → zapc → promc
在前，本组件最后接线）：

```go
pm, err := AddComponent(t, r, "promc", promc.Default(), promc.New)
if err != nil {
    return err
}
hs, err := AddComponent(t, r, "httpserver", httpserver.Default(),
    func(c httpserver.Config) (*httpserver.Server, error) {
        return httpserver.New(c,
            httpserver.WithService(meta.Name, meta.Env, version.Version),
            httpserver.WithProm(pm)) // 单端口收编 /metrics /health
    })
if err != nil {
    return err // 配置非法在此报错（退出码 1）
}
// 业务路由一律在装配期注册（Start 后注册 panic）：
hs.HandleFunc("/api/v1/greet", func(w http.ResponseWriter, r *http.Request) {
    id := httpserver.RequestID(r.Context()) // 中间件注入的 RequestID
    _ = id
})
```

nacos 注册等需要实际端口的装配点用 `hs.Addr()`（`:0` 由内核分配，
Start 后取实际值）。

**配置节**（追加到 `configs/config.yaml`，或由
`config.Dump(os.Stdout, "httpserver", httpserver.Default())` 生成；完整
样例见 `configs/config.yaml`）：

```yaml
httpserver:
  addr: ":8080"
  read_timeout: 10s        # "10s" / "1m30s" / 裸整数（按秒）；0 = 无
  write_timeout: 30s
  idle_timeout: 120s
  max_header_bytes: 1048576
  max_body_size: 10485760  # 超限 413；0 = 不限制
  trusted_proxies: [127.0.0.0/8, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, ::1/128]
  liveness: /livez
  readiness: /readyz
  versions: /version
  cert_file: ""            # 空 = HTTP；非空与 key_file 成对启用单监听 HTTPS
  key_file: ""
  pprof: true
  drain_aware: true
  cors:
    allowed_origins: ["*"]
    allowed_methods: [GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS]
    allowed_headers: ["*"]
    allow_credentials: false  # true 与 "*" origins 互斥（构造期拒绝）
    max_age_seconds: 7200
```

## 内置端点

| 路径 | 行为 |
|---|---|
| `/livez` | 存活探针；停机排空期保持 200（摘流由 readiness 承担） |
| `/readyz` | 就绪探针；排空中 503（draining），否则聚合 promc 健康检查（任一失败 503，未注入 promc 时恒 200） |
| `/version` | `pkg/version` 五字段 JSON |
| `/debug/pprof/*` | 标准 pprof 端点组（`pprof: false` 关闭） |
| promc 的 `metrics_path` / `health_path` | `WithProm` 注入时挂载（路径以 promc 配置为单一事实源） |

## 行为契约

- **访问日志**：`httpserver_request_completed`（zap 直调），字段
  `method / path(不含 query，截 512) / route(匹配模板) / status_code /
  duration_ms / client_ip / trace_id / request_id / user_agent(截 256) /
  referer(截 256，缺省省略) / request_bytes / response_bytes(实测字节)`；
  级别 5xx→Error、4xx→Warn、其余 Info。跳过清单（探活/就绪/版本/
  metrics/health/pprof）不记日志不计指标，但 span 照起、实例头照注入
- **panic**：`httpserver_panic_recovered`（Error，含 stack 与请求字段），
  响应未开头时写 500 JSON；panic 请求不进常规访问日志与指标（信息由
  专门日志承载）——Recovery 在链最外层，含 CORS 层的 panic 一并捕获
- **RequestID**：`X-Request-ID` 合法（`^[A-Za-z0-9._-]{1,64}$`）→ 注入
  ctx（`pkg/ctxkey`，otelc 日志装饰自动附带 request_id）+ 原样回写
  响应头；缺失/非法静默忽略，TraceID 兜底（与 span 同源），无链路时
  crypto/rand 32 hex——恒有值。业务侧 `httpserver.RequestID(r.Context())`
- **TraceID**：span 有效时响应头 `X-Trace-ID` 回显本服务端 span 的
  TraceID（可信继承与新 root 同口径；外部传入的 traceparent 不回显，
  防伪造值借响应头回流）——响应、日志、链路三方凭同一 TraceID 互查
- **实例 ID**：所有响应带 `X-Instance-IDs`；值 = `InstanceID(service)`
  （`INSTANCE_ID` 环境变量 > `{service}:{HOSTNAME}` > `{service}:unknown`）
  + `extra_instance_ids` 逗号拼接。同一值应经 `otelc.WithInstanceID` 进
  OTel resource（biz.go 已如此接线）
- **客户端 IP**：RemoteAddr ∈ trusted_proxies 才解析 XFF（从右剥离可信
  代理取首个不可信 IP，全可信取最左）；直连时外部 XFF 一律无视。
  不读 X-Real-IP
- **停机三步走**（drain_aware，缺省开）：readiness 翻 503 摘流 →
  `SetKeepAlivesEnabled(false)`（响应带 `Connection: close`）→
  `Shutdown(ctx)` 排空在途请求；幂等，Shutdown 失败降级警告
- **请求体上限**：声明 ContentLength 超限直接 413；chunked/谎报经
  `http.MaxBytesReader`（业务读到错误，未写响应时 net/http 自动 413）
- **指标直连 prometheus**：`httpserver_` 前缀注册 promc registry；
  pattern 标签用路由模板（未匹配记 `unmatched`），基数受路由数约束
- **fail-fast**：配置非法、TLS 证书不可加载、端口占用、内置路径冲突、
  CORS 凭据与通配互斥，均在构造期或启动期拒绝
- **路由冻结**：业务路由仅 Start 前注册（Handle 之后 panic——装配期
  暴露）；运行时动态注册不在模板范围
- **热更**：`max_body_size` / `trusted_proxies` / `drain_aware` / `cors.*`
  原子生效；其余冷字段变更记警告 `httpserver_config_restart_required`
  后不生效（重启生效）——`http.Server` 运行中改字段属数据竞争，监听器
  重建（换端口/换证书）不做热更
- **并发纪律**：热更态全走原子变量（代理网段表/CORS 实例整体快照替换）；
  中间件链 Start 时组装一次；单请求的共享状态（reqState）串行访问
- **已知限制**：HTTP+HTTPS 双监听、TLS 证书热轮换、请求体multipart
  细粒度校验均不在范围；公网部署应收紧 `pprof`、`cors.allowed_origins`
  与 `trusted_proxies`

## 字段速查

| 字段 | 缺省 | 热更 | 说明 |
|---|---|---|---|
| addr | ":8080" | 否 | 监听地址（`:0` 内核分配，`Addr()` 取实际值） |
| read_timeout | 10s | 否 | 读超时（header+body 全程）；0 = 无 |
| write_timeout | 30s | 否 | 写超时（含慢客户端下载）；0 = 无 |
| idle_timeout | 120s | 否 | keep-alive 空闲超时；0 = 无 |
| max_header_bytes | 1MB | 否 | 请求头上限；0 = net/http 缺省 |
| max_body_size | 10MB | 是 | 请求体上限；超限 413；0 = 不限制 |
| trusted_proxies | loopback+私网段 | 是 | 可信代理网段（XFF 剥离与 traceparent 信任判定共用） |
| liveness | /livez | 否 | 存活探针路径 |
| readiness | /readyz | 否 | 就绪探针路径 |
| versions | /version | 否 | 版本信息路径 |
| cert_file / key_file | "" | 否 | 成对非空启用单监听 HTTPS |
| pprof | true | 否 | 暴露 /debug/pprof/* |
| drain_aware | true | 是 | 排空期感知停机 |
| extra_instance_ids | [] | 否 | 追加实例 ID（逗号拼进 X-Instance-IDs） |
| cors.* | 见上 | 是 | 跨域子节（rs/cors） |

## 生命周期 API

| API | 说明 |
|---|---|
| `Default() Config` | 默认值基座（与 `config.Decode` 成对使用） |
| `(Config).Validate() error` | 校验取值（非空 / 路径合法互异 / 网段可解析 / CORS 互斥规则） |
| `New(cfg Config, ...Option) (*Server, error)` | 构造即校验并挂载内置端点（不监听）；失败无资源需清理 |
| `WithService(name, env, ver string) Option` | 服务标识：服务名进实例 ID 与启动日志 |
| `WithProm(p PromProvider) Option` | 注入 promc（单端口收编接线点；接口满足即零 import） |
| `(*Server).Start(ctx) error` | 组装中间件链 + 同步监听（绑定错误 fail-fast） |
| `(*Server).Stop(ctx) error` | 三步走优雅停机；幂等，失败降级警告 |
| `(*Server).ApplyConfig(cfg Config) error` | 热更（范围见上）；冷字段警告后跳过 |
| `(*Server).Handle / HandleFunc(pattern, h)` | 注册业务路由（仅 Start 前；非法 pattern 当场 panic） |
| `(*Server).Addr() string` | 实际监听地址（nacos 注册等装配点用） |
| `RequestID(ctx) string` | 业务侧取 RequestID（中间件注入） |
| `InstanceID(service string) string` | 实例 ID 解析（响应头/OTel resource/启动日志单一事实源） |
