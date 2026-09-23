# Go 服务模板 · 设计文档

> 状态：设计定稿，无待定项。本文与仓库内 `CONTEXT.md`（术语表）共同自包含：仅凭二者即可从零实现模板与组件资产，不需要参考任何其他项目。

## 1. 定位与消费方式

**是什么**：Go 长驻服务的项目模板，覆盖五项基础能力——命令、应用元数据、配置、日志、优雅停机。其余一切功能以**组件**
形态按需引入：内置组件库（`internal/components`，随模板分发、删留自便）与组件资产（独立 Go module，见 §11/§12）两种载体。

**目标业务形态**：HTTP API、gRPC、消息消费者、定时任务及其混合。一次性任务以兄弟子命令存在（§6），不使用热更与停机设施；纯 CLI
工具不是目标形态。

**不是什么**：不是框架、不是可 import 的依赖库。模板被复制后即为项目自有代码，可自由修改；模板对使用方式没有任何运行时约束（无注册、无发现、无注入机制）。

**消费方式**——从模板落地新项目：

1. 复制本仓库全部内容（不含 `.git`）到新项目目录
2. 改 `go.mod` 的 module 名（如 `github.com/you/your-service`），全局替换 import 路径
3. `go build ./... && go test ./...`
4. 按需引入组件资产：`go get` + 在装配入口接线（远程源 → `setupSources`，业务组件 → `setupBiz`；§4/§11）+ 粘贴配置节
5. `go run ./cmd/app`，Ctrl+C 验证优雅退出

**依赖原则**（单向，环为零）：

```
业务项目（模板复制体） ──import──▶ 组件资产 ──import──▶ observ（可选）
        │
        └─ 模板自有代码（config / app / cmd）不 import 任何组件
```

- 组件无法依赖模板——模板是复制型资产，落地后每个项目的 module 路径都不同，依赖天然单向（项目 → 组件）
- 组件的依赖不受模板约束：组件（尤其第三方库）依赖什么由其自定，模板不设限；observ 只是原生组件的**推荐**抽象面，不是准入门槛
- 模板自有代码不 import 任何组件；远程源与业务组件的引入只发生在两处装配入口：`internal/app/sources.go` 的 `setupSources`、
  `internal/app/biz.go` 的 `setupBiz`（§4、§11）

## 2. 验收标准

1. **落地 5 步**：复制 → 改 module 名 → build → run → Ctrl+C 优雅退出（退出码 0）
2. **引入组件触点**：内置组件直接在装配入口接线（无需 go get，见各组件 README）；组件资产 `go get` + 装配入口一处（远程源 →
   `setupSources`；业务组件 → 业务入口 `biz.go` 的 `AddComponent`，或等价手写展开）+ 配置节粘贴（无配置组件省略）；*
   *不修改模板任何既有文件**（`setupSources` 为预留空实现；`setupBiz` 内置占位业务 `biz.Hello`，替换该包即接入真实业务）
3. **依赖随删随清**：模板与内置组件的依赖随组件进入 go.mod；删除不需要的组件目录并 `go mod tidy` 后，go.mod
   直接依赖即收敛为实际使用集（骨架基线四件：cobra、gopkg.in/yaml.v3、fsnotify、observ）
4. **热更可演示**：修改 `log.level` 保存即生效（无需重启）；引入示例组件（internal/components/greeter）后其配置节热更同样可演示
5. **占位基线**：模板原样运行 = announce 启动行 + 占位业务一行（`biz_started`）→ 静默等待信号 → 预算内干净退出；两组件同场演示多组件组合与启停顺序
6. **fail-fast**：任一组件构造或启动失败 → 已启动者逆序停止 → 退出码 1
7. **模板自带测试**：`go test ./...` 覆盖难点单测——config 包（合并分层、from_env 收集与类型推断、严格解码、Watch
   收敛/合并/取消、多环境文件名推导）、runner（顺序启动/逆序停止、Start 失败回滚、停机预算）、logging（level 热更）、`AddComponent`
   （测试内 stub 组件）、可观测组件（otelc 空 endpoint 模式与日志 trace 注入、promc 端点聚合与 Meter 闭环）；信号触发路径仅
   POSIX build-tag 测试。验收 5 步保持手动演示。

## 3. 术语表

见根目录 [`CONTEXT.md`](../CONTEXT.md)（单一事实源，随代码演进维护）。

## 4. 架构总览

```
main.go（3 行：internal/cmd.Execute()）
 └─ cobra root = run（默认命令）
     └─ app.Run（唯一启动时序）
         1. config.Load     读本地两层文件（基础 + 多环境）→ 配置树 + 文件监听 + from_env 静态层
         2. setupSources(t) 接入远程配置源（Attach，首快照同步）。
                            先于日志装配——log 节与元数据初值因此含远程层（format/output
                            等非热更字段方能由远程治理）；引导自配只来自本地层（§8.2）
         3. setupLogging    设 observ 默认日志后端 + log 节 level 热更订阅
         4. meta            解析应用元数据（name / 生效 env），注册 announce 启动行组件
                             （首个启动者，§7）
         5. setupBiz(t, r, meta) 装配业务组件（业务入口 biz.go）：解码配置节 → 构造 →
                             注册生命周期 →（可选）热更订阅；
                             meta 供需要元数据的组件使用（如注册组件传 meta.Name）
         6. r.Run()         信号 → root ctx → 顺序 Start → 阻塞等待 → 逆序 Stop（预算内）
```

| 层   | 成员                                                                                       | 职责                            |
|-----|------------------------------------------------------------------------------------------|-------------------------------|
| 命令层 | `internal/cmd`（cobra）                                                                    | 参数解析、子命令、进程退出码                |
| 装配层 | `internal/app`（Run / setupSources+setupBiz / metadata+announce / logging / AddComponent） | 启动时序、源与组件装配、优雅停机              |
| 运行器 | `internal/runner`（Runner：Add / StartAll / StopAll / Run）                                 | 生命周期机制：顺序启动、逆序停止、信号、预算        |
| 配置层 | `internal/config`                                                                        | 加载、合并、节读取、热更总线、Source 接口、Dump |
| 组件层 | 外部资产（独立 module）                                                                          | 一切业务与基础能力                     |

引导失败（config.Load、远程源接入、日志装配、业务装配任一步出错）时日志可能未就绪：错误信息直写 stderr，进程退出码 1。

## 5. 目录布局

```
<module>/
├── README.md                  # 五步 quickstart + 组件引入指引（指向 docs/）
├── LICENSE                    # MIT
├── cmd/app/main.go            # 3 行：internal/cmd.Execute()
├── internal/
│   ├── app/
│   │   ├── app.go             # Run()：config → sources → logging → meta → biz → runner 时序
│   │   ├── logging.go         # 日志装配：observ 默认后端 + level 热更（换 zap 的唯一改动点）
│   │   ├── metadata.go        # app 节 Meta + announce 启动行组件（首个启动者；版本自 pkg/version）
│   │   ├── biz.go             # 业务装配入口：业务组件接线（内置占位业务 biz.Hello，业务逻辑定位点）
│   │   ├── sources.go         # 远程源接入 setupSources（模板内为空实现）
│   │   └── component.go       # AddComponent 装配辅助：解码 → 构造 → 注册 → 可选热更
│   ├── runner/
│   │   └── runner.go          # 运行器：顺序启动、逆序停止、信号、停机预算（§10）
│   ├── cmd/
│   │   ├── root.go            # run（默认命令）+ --config / --env / --log-level
│   │   └── version.go         # version 子命令（打印 pkg/version 五字段）
│   ├── biz/
│   │   └── hello.go           # 占位业务组件（启动输出一句日志；项目替换为真实业务）
│   ├── components/            # 内置组件库（组件菜单，依赖不设限；取舍规则见 §12 与库内 README）
│   │   ├── greeter/           # 约定完整示范样例（附录 A 指向此处）
│   │   ├── nacos/             # nacos 双角色客户端（配置中心 Source + 服务注册）
│   │   ├── zapc/              # zap 日志组件（级别热更即时生效，其余变更重建实例）
│   │   ├── otelc/             # OTel 可观测组件（tracing + 日志 trace 注入 + OTLP 日志导出，附录 D）
│   │   ├── promc/             # 指标与健康检查组件（prom registry + observ.Meter 适配，附录 D）
│   │   └── httpserver/        # 业务 HTTP Server 组件（可观测中间件链 + 单端口收编，附录 D）
│   └── config/
│       ├── config.go          # Load / Tree / Raw / Decode / Section 句柄 / Dump
│       ├── source.go          # Source 接口 + 文件监听 + 合并管线
│       ├── overlay.go         # from_env 收集与静态覆盖
│       └── bus.go             # 节级订阅与串行分发
├── pkg/
│   ├── version/               # 构建期版本元数据（ldflags 注入：version/commit/date/build_time/go_version）
│   ├── safe/                  # 日志安全整形：脱敏掩码（保长/折叠）与截断（展示/体积），纯函数
│   ├── ctxkey/                # 跨组件共享的 context 键（request_id：httpserver 注入、otelc 日志装饰消费）
│   └── constant/              # 跨包原子常量（时间布局等）
├── configs/config.yaml        # app: / log: / biz: 与组件节示例（zapc、otelc、promc、httpserver 等）
├── CONTEXT.md                 # 术语表（单一事实源）
└── docs/                      # DESIGN.md / ASSETS.md / adr/
```

缺省 `configs/config.yaml` 内容（log 三字段全量写出，文档价值优先；`env` 不写即不选多环境文件）：

```yaml
app:
  name: demo
log:
  level: info
  format: text
  output: stdout
```

- Go 版本要求：1.25+
- 模板 go.mod 第三方依赖白名单：`github.com/spf13/cobra`、`gopkg.in/yaml.v3`、`github.com/fsnotify/fsnotify`、
  `github.com/jninng/observ`

## 6. 命令体系

CLI 库为 cobra。命令集两个，刻意收敛：

| 命令                    | 行为                                                                  |
|-----------------------|---------------------------------------------------------------------|
| `run`（root 默认，无参数即执行） | 完整启动时序（§4），阻塞至信号，返回值决定退出码                                           |
| `version`             | 打印 `pkg/version` 五字段（构建期 ldflags 注入，见 §7）；逐行对齐输出，不带 name/env 前缀        |

**run 的 flag**（影响配置的唯一入口）：

| flag          | 缺省                    | 语义                                |
|---------------|-----------------------|-----------------------------------|
| `--config`    | `configs/config.yaml` | 基础配置文件路径                          |
| `--env`       | 读 `APP_ENV`           | 选定多环境文件（§8）；显式指定而文件缺失 → fail-fast |
| `--log-level` | 无                     | 静态覆盖 `log.level`，置合并栈顶（§8）        |

**扩展一次性子命令**：在 `internal/cmd` 增加普通 cobra 子命令即可（如数据修复、迁移任务）。兄弟子命令自行管理生命周期与
`os.Exit`，不经过 runner、不享受热更与优雅停机。

**退出码**：

| 场景               | 码 |
|------------------|---|
| 信号触发、预算内完成停机     | 0 |
| 任一组件构造或 Start 失败 | 1 |
| 停机超总预算被强杀        | 1 |
| 收到第二个信号被强杀       | 1 |
| 引导期（配置/装配）失败     | 1 |

## 7. 应用元数据

- **`app:` 节**（模板自持定义，位于 `internal/app/metadata.go`）：
    - `name`（string，必填非空）：应用名。缺失或为空 → 启动 fail-fast。
    - `env`（string，可选）：运行环境**声明值**，供下游消费（启动日志、可观测资源、注册分组）。
- **生效 env 的解析顺序**：`--env` > `APP_ENV` > `app.env` 声明值 > 空。前两者同时是**唯一**有权选择多环境文件的输入（避免"
  配置里改 env 换文件"的循环依赖）。
- **版本元数据**：`pkg/version` 五字段（version / commit / date / build_time / go_version），
  构建期 ldflags 注入（date 为源码提交日期、build_time 为构建时刻；go_version 随二进制
  自述无需注入），不进配置文件。消费方：version 子命令、启动行 `app_version`、OTel 资源
  `service.version`：
  ```
  go build -ldflags "-X '<module>/pkg/version.Version=v1.2.3'" ./cmd/app
  ```
- **启动行**：由 **announce 组件**承载（首个注册、首个启动，§4/§10）：
  `observ.DefaultLogger().Log(ctx, slog.LevelInfo, "service_started", slog.String("app_name", …), slog.String("app_env", …), slog.String("app_version", …))`
  （消息与字段 snake_case，见 §9 日志规范），模板运行的最小可见信号。
- **消费方式**：元数据是纯数据。组件需要它时由装配点显式传参（如注册组件的 service name 传 `meta.Name`），不存在元数据广播机制。

## 8. 配置体系

### 8.1 文件与定位

- 基础文件：`--config` 指定，缺省 `configs/config.yaml`。缺失 → fail-fast。
- 多环境文件：生效 env 非空时，在基础文件同目录、扩展名前插 `.<env>`（`config.yaml` + `--env prod` → `config.prod.yaml`
  ）。显式指定而文件缺失 → fail-fast。多环境文件与基础文件享有同等的热更监听。
- 文件监听实现要求：监听父目录并按路径过滤——fsnotify 直监听文件会漏掉 symlink 替换（k8s
  ConfigMap）与编辑器原子写（temp+rename）两类事件。
- 格式：yaml。顶层节 = 组件领地；`app` 与 `log` 归模板，组件节名不得与之冲突（装配者保证，模板不做校验）。

### 8.2 合并分层（低 → 高）

```
代码默认值（Decode 的基座实例）
  < 基础文件
  < 多环境文件
  < 远程配置源（Source 快照）
  < from_env 环境变量绑定（静态）
  < flag 覆盖（静态，仅 --log-level）
```

- 合并语义：map 深合并；标量与数组整体覆盖，不做数组拼接。
- **静态覆盖层**（from_env、flag）在启动时一次性生效，其后文件与远程的任何变更都不改写其结果；每次树重建时静态层重新套用。
- 热更引发的每次树重建与重解码，都按同一分层重新合并——代码默认值始终是基座、静态层始终在栈顶，**运行时覆盖与启动时同构**
  （§11.5 的 `AddComponent` / `config.Watch` 重解码即依赖此性质）。
- 远程源对配置的解析失败：记日志丢弃该快照，维持上一有效树（全量快照语义下最终一致）。本地文件变更解析失败同理。
- `nacos.config` 一类的"引导自身所需"配置只能来自本地层（读它时远程尚未连通），由组件文档声明，模板不特殊处理。

### 8.3 读取 API

```go
// internal/config
type Tree struct{ /* 合并结果 + 总线，并发安全 */ }

func Load(configPath, env string, overrides ...Override) (*Tree, error)
//   读两层本地文件 → 收集 from_env → 应用静态覆盖；启动文件监听。
//   Override{Key string, Value any}：点路径键，置于栈顶（run 命令传 --log-level 用）。

func (t *Tree) Attach(src Source) error
//   启动远程源；其全量快照合并于本地之上、静态层之下。
//   同步语义：等待首份快照到达并合并完成后才返回（连接错误已在客户端构造时处理）。
//   Source.Start 返回 error 时 Attach 原样返回 → 装配点 fail-fast；
//   运行期（首快照之后）的错误由源自行处理，不静默吞。

func (t *Tree) Raw(section string) (map[string]any, bool)

func Decode[T any](t *Tree, section string, base T) (T, error)
//   泛型解码：base 为默认值基座，节内键覆盖之。
//   严格解码（未知键报错，yaml KnownFields 语义）：拼写错误在启动期暴露。
//   节缺失 → 返回 base 原样（组件以全默认值运行），无 error。
//   from_env 保留键在解码前剔除。

type Section[T any] struct{ /*（树，节名，默认值基座）三元组，并发安全 */ }

func Bind[T any](t *Tree, name string, base T) *Section[T]
//   把节绑定为运行时句柄：只登记三元组，不读配置、不失败（解析推迟到 Get）。
//   name 引组件的 SectionName 常量，避免字面量漂移。

func (s *Section[T]) Name() string

func (s *Section[T]) Get() (T, error)
//   运行时拉取该节当前合并生效值（pull；与 Watch 的 push 互补，偶发
//   读取不必常驻订阅）。与 Decode / Watch 重解码同一路径，三者结果恒一致；
//   严格解码、节缺失回落 base。

func Watch[T any](t *Tree, section string, base T, apply func(T) error) (cancel func())
//   节级热更订阅：建立时立即以当前值调用一次 apply（收敛语义），
//   其后仅该节变更时调用；节消失视为"变更回默认值"（以 base 调用）。
//   解码失败（含未知键）→ 记日志整体丢弃本次、保持上一有效值；apply 返回 error → 记日志，进程不死。

func Dump(w io.Writer, section string, cfg any) error
//   把任意组件默认值渲染为可粘贴的 yaml 配置节（§11 手动复制用）。
```

### 8.4 from_env（唯一保留键）

- 配置节内保留键 `from_env`：值为逐层 map，叶子为环境变量名，声明"配置键 ← 环境变量"。
- 启动时从**本地层**（基础 + 多环境文件）收集一次全部声明；环境变量**非空**才覆盖对应键（未设或空串不生效）。
- 覆盖值做 bool/int/float 尽力类型推断，否则字符串。
- 远程下发的 `from_env` 声明忽略（实例级绑定属部署决策，不下发自远程）。
- 声明本身的热更不生效（重启生效）。
- 白名单式声明可审计、拼写错误在声明处可见；模板不做 `APP_XXX` 类约定前缀扫描。

### 8.5 远程配置源（Source）

```go
type Source interface {
    Name() string
    // Start 阻塞或内部自管；每次配置变更以全量快照调用 push（非增量）。
    // ctx 取消即停止。快照中消失的节即视为删除。
    Start(ctx context.Context, push func(map[string]any)) error
}
```

- Source 由组件构成（内置组件或资产，如 nacos 客户端），模板只认此接口；Source 的签名全部由朴素类型构成，**可由结构化类型满足
  **——资产零 import 模板、暴露同签名方法即可直传 `Attach`，无需适配胶水（接入示例见附录 B）。
- 不可达策略（fail / disable）是组件资产的客户端选项，不是模板机制。
- **装配纪律——先源后一切**：源接线（`setupSources`）先于日志装配与一切组件装配；配合 `Attach`
  的首快照同步语义，日志初值与组件初值都总是完整的"本地 + 远程 + 静态层"合并结果，不存在"后附源靠热更收敛"的时序歧义（非热更字段如
  `log.format` 也因此可由远程治理）。

### 8.6 热更总线契约

- **串行有序**：树替换与通知全局同序；订阅者最终状态恒等于 `Raw` 读到的状态。
- **逐订阅投递**：每个订阅在专属 goroutine 内串行回调，订阅之间互不阻塞；缓冲深度 1，突发变更合并为最新值。
- **收敛语义**：`Watch` 建立即以当前值首调一次；`apply` 必须幂等（相同值无操作）。
- **隔离**：apply 内 panic 被 recover 并记日志；apply 应快速返回，重活自行异步。
- **取消**：`cancel()` 返回后该订阅无在途且无后续回调。
- 初始配置永远在装配期以 `Decode` 显式读取；`Watch` 建立时的收敛首调与 `Decode` 结果一致（幂等实现下无操作），真正的变更投递只发生于其后——收敛首调同时封住
  Decode 与订阅建立之间的竞态间隙。

## 9. 日志

**单一调用面**：模板代码严格经 **observ.Logger**（`Enabled(ctx, slog.Level) bool` /
`Log(ctx, level, msg string, attrs ...slog.Attr)`，级别与属性复用 slog 类型；observ v0.2.0 起两方法携带 ctx、签名与
slog.Logger 逐字对齐——链路上下文由此流到实现侧，见下文"链路关联"）。组件不强制：原生组件推荐同走 observ
约定（§11），第三方组件按其日志面经适配层桥接（见下表）。装配点设置 observ 默认后端：缺省实现零配置可用（基于 stdlib 构建）；业务换
zap 时在装配点一处换向，业务自身代码直调 zap——不经 observ、不经任何中间层，高频路径零额外开销。

**持有规则**：

- **模板包**：不持有 logger 字段，调用点动态读 `observ.DefaultLogger()`（atomic 读，无锁；模板无高频路径，读取代价可忽略）。原因：config
  包的构造早于日志装配（`log:` 节在配置里，先有配置后有后端），构造期快照会永久固定在 Noop；动态读同时保证换后端对已构造的模板设施立即生效。
- **组件资产**：不做统一要求。原生组件推荐按 observ 规范——`WithLogger(observ.Logger)` option 显式注入（测试捕获用），未注入时构造期快照
  `observ.DefaultLogger()`，组件构造发生在装配点、晚于后端设置，快照即正确后端。**源角色组件例外**：在 `setupSources`
  构造（早于日志装配），快照会永久固定在 Noop——此类组件须动态读 `observ.DefaultLogger()`
  （低频路径，代价可忽略），不可达降级类高信号告警宜双通道（observ + 直写 stderr）保底（nacos 组件即此形态）。第三方组件按其自身日志面经适配层桥接（见下表）。

**装配点**（`internal/app/logging.go`，换后端的唯一改动处）：

```go
// setupLogging：log 节 → 缺省后端 → 设 observ 默认 → level 热更
// 非法 level（启动期）与构造错误 → 返回 error（引导失败 fail-fast）
func setupLogging(t *config.Tree, r *runner) error {
    cfg, err := config.Decode(t, "log", logDefault())   // log 节定义自持于 app 包
    if err != nil { return err }
    lvl := new(slog.LevelVar)
    lvl.Set(parseLevel(cfg.Level))
    w, closeFn := outputWriter(cfg.Output)                  // stdout 或文件（仅追加）
    slog.SetDefault(slog.New(buildHandler(cfg.Format, lvl, w)))
    observ.SetDefaultLogger(observ.NewSlogLogger(slog.Default()))
    if closeFn != nil {                                     // output 为文件：停机关闭钩子（逆序最后执行）
        r.Add("log-close", nil, func(context.Context) error { return closeFn() })
    }
    config.Watch(t, "log", logDefault(), func(c logConfig) error {
        if next, err := parseLevel(c.Level); err != nil {
            return err                                      // 非法热更值：记 warn 保持旧值
        } else { lvl.Set(next) }                            // 唯一热更字段（作用于后端级别）
        return nil                                          // 其余字段变更记 info 提示重启
    })
    return nil
}
```

**`log:` 节字段**（模板自持默认值：level=info、format=text、output=stdout）：

| 字段       | 取值                          | 热更                          |
|----------|-----------------------------|-----------------------------|
| `level`  | debug / info / warn / error | **是**（唯一热更字段，作用于后端级别，调用面无感） |
| `format` | text / json                 | 否（重启生效）                     |
| `output` | `stdout` 或文件路径              | 否（重启生效）                     |

**非法 level 值**：启动期（flag 或文件）`level` 非法 → 构造失败 fail-fast；热更收到非法值 → 记 warn 保持旧值（Watch
契约：解码失败丢弃本次）。

**output 为文件**：logging 装配注册停机钩子关闭文件（stdlib handler 无缓冲，纯卫生，不丢数据）。

**不做文件轮转**：`output` 文件仅追加。轮转归属部署侧（logrotate / 容器 runtime）或业务换入的 zap 方案——这是缺省日志链路保持零第三方依赖的边界。

**原生组件如何拿到日志**：不显式传递。启动时序保证 setupLogging 先于组件装配，组件构造期快照 `observ.DefaultLogger()`
即正确后端；显式 `WithLogger` 注入保留给测试。模板与原生组件共享同一个包级默认，无传递机制。第三方组件不经此路径，按其日志面形态桥接（见下表）。

**第三方库的日志面**（适配组件的桥接规则，按库接口形态三选一）：

| 库的日志接口            | 接法                                                                    |
|-------------------|-----------------------------------------------------------------------|
| 接收 `*slog.Logger` | 传 `slog.Default()`——缺省后端即它；换 zap 时可选地把 slog.Default 一并重指向（见配方末行），零额外桥 |
| 自有 logger 接口      | 适配层以 observ.Logger 实现该接口（几行委托代码）                                      |
| 接收 zap 等具体后端      | 直接传该后端实例，不绕 observ                                                    |

**换 zap 配方**（`logging.go` 一处替换；业务代码直调 zap）：

```go
z := zap.Must(zap.NewProduction())
observ.SetDefaultLogger(zaplog.New(z))   // 模板与组件全部换向（observ/adapters/zaplog）
// 装配点补停机冲刷：
// r.Add("zap-sync", nil, func(context.Context) error { _ = z.Sync(); return nil })
// 可选：把仍走 slog.Default() 的第三方库重指向 zap（zap 生态的 slog handler）
```

业务代码直调 zap，不经任何桥接层；模板与组件经 zaplog 适配器直抵 zap，零改动（动态读与装配期换向都指向新后端）。level 热更由
zap 动态级别承接（如 `zapcore.NewAtomicLevel`）。

**Meter**：骨架自身零装配代码——组件经 observ.Meter 埋点时缺省 Noop、零开销；observ v0.3.0 起 Meter 与 Logger 同款包级默认
（`DefaultMeter`：原子替换、初始 Noop、构造期快照）。内置组件库的 **promc**（附录 D）提供开箱即用的出口：私有
Prometheus registry + `/metrics` `/health` 端点，以 `adapters/prom` 实现 `observ.Meter`，接线即安装为包级默认——
其后构造的业务组件未注入 `WithMeter` 时构造期回落（与日志同一注入规范；显式注入覆盖）。不接线 promc 的项目维持
Noop 缺省，模板行为不变。

**链路关联**：内置组件库的 **otelc**（附录 D）在装配后自动为日志注入链路属性——ctx 携带有效 span 的日志调用附加 `trace_id` /
`span_id`（注入做在 observ 边界的装饰层，不接管任何日志全局；无 span 时零属性差异）。该能力依赖 observ v0.2.0 的 ctx
签名；模板调用点因此始终传真实 ctx（基础设施路径无业务 span，传 `context.Background()`）。

**日志规范**（模板自有代码执行，原生组件建议同遵）：

- 消息与字段一律 snake_case。消息命名 `{模块}_{动作}_{状态}`，后缀：操作失败 `_failed`（默认）、校验/状态异常 `_error`、正常态
  `_success` / `_started` / `_completed`；字段必须带业务前缀（`app_name`、`file_path`、`error`），禁 `id` / `name` / `msg`
  等模糊名。
- 级别：技术故障（IO/连接/配置加载/panic）= Error 且自动附 `stack`（仅在错误最底层打一次）；业务与校验异常（热更值被拒等）=
  Warn；关键流程节点 = Info。配置值与敏感信息不落日志。
- 并发安全：后端换向经 observ 原子替换，级别热更经 slog LevelVar——不存在对全局 logger 的直接重赋值。

## 10. 优雅停机

运行器是模板唯一的生命周期机制，位于独立机制包 `internal/runner`（装配层仅经 `runner.New()` 使用），参考实现：

```go
// internal/runner/runner.go（package runner，机制独立于装配层）
package runner

import (
    "context"
    "fmt"
    "log/slog"
    "os"
    "os/signal"
    "syscall"
    "time"

    "github.com/jninng/observ"
)

const (
    defaultStepTimeout  = 15 * time.Second // 单组件停止预算缺省
    defaultTotalBudget  = 30 * time.Second // 停机总预算缺省
)

// entry 是一个已注册组件的启停对。
type entry struct {
    name  string                      // 组件名（装配时给定，用于日志与错误归因）
    start func(context.Context) error // 启动钩子，nil 表示跳过
    stop  func(context.Context) error // 停止钩子（须幂等），nil 表示跳过
}

// Runner 按注册顺序启动、逆序停止所辖组件；信号与停机预算由其统一管理。
type Runner struct {
    entries      []entry       // 注册序即启动序，逆序即停止序
    started      int           // 已成功启动的组件数（StopAll 的停止范围）
    stepTimeout  time.Duration // 单组件停止预算
    totalBudget  time.Duration // 停机总预算
}

// New 创建空运行器。
func New() *Runner { return &Runner{} }

// Add 注册一对生命周期；start / stop 均可为 nil（nil 跳过）。
// 注册顺序即启动顺序，逆序即停止顺序。
func (r *Runner) Add(name string, start, stop func(context.Context) error) {
    r.entries = append(r.entries, entry{name, start, stop})
}

// Names 返回已注册组件名（注册顺序）；诊断与测试用。
func (r *Runner) Names() []string { ... }

// StartAll 顺序启动全部组件；任一失败即逆序停止已启动者并返回错误
// （fail-fast 点）。Run 的启动半程，供需要手动控制生命周期的场景。
func (r *Runner) StartAll(ctx context.Context) error {
    r.started = 0
    for _, e := range r.entries {
        if e.start != nil {
            if err := e.start(ctx); err != nil {
                _ = r.stopStarted()
                r.started = 0 // 已回滚：后续 StopAll 不再重复执行
                return fmt.Errorf("start %s: %w", e.name, err)
            }
        }
        // nil-start 条目也计入停止范围：其资源在装配期已建立
        r.started++
    }
    return nil
}

// StopAll 逆序停止已启动组件；预算耗尽即强杀（退出码 1）。
func (r *Runner) StopAll() error {
    err := r.stopStarted()
    r.started = 0
    return err
}

// Run 阻塞执行：安装信号处理 → 顺序启动 → 等待信号/取消 → 逆序停机。
func (r *Runner) Run() error {
    sigCh := make(chan os.Signal, 2)
    signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
    defer signal.Stop(sigCh)

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    go func() { // 第一个信号 → 优雅停机；第二个信号 → 立即强杀
        <-sigCh
        cancel()
        <-sigCh
        os.Exit(1)
    }()

    if err := r.StartAll(ctx); err != nil {
        return err
    }

    <-ctx.Done()
    return r.StopAll()
}

// stopStarted 逆序停止前 started 个已启动组件（单步预算内执行，细节从略）：
// Stop 错误仅记日志（component_stop_failed，Error + stack）不中断流程；
// 预算耗尽记 shutdown_budget_exhausted 后 os.Exit(1)。
```

契约要点：

- **顺序**：注册顺序 = 启动顺序（依赖即书写顺序）；逆序 = 停止顺序。
- **Start 语义**：起 goroutine 后立即返回；返回 nil 即"可用"。
- **Start 无预算**：停机预算只承诺 Stop；Start 阻塞卡死以第二个信号强杀为唯一逃生门——有意接受的边界。
- **Stop 语义**：必须幂等（可安全重入）；ctx 携带单步预算，超时由组件自行截断返回。
- **预算**：单步 15s、总 30s 缺省（为 httpserver 的真实排空时间放宽）；
  `SetBudgets` 可调（Run 之前），仍刻意不设 yaml 配置面——预算是
  部署形态属性，由装配层表达。
- **信号**：SIGINT / SIGTERM → cancel root ctx；第二个信号立即 `os.Exit(1)`；SIGQUIT 保留 Go 默认栈转储。
- **无容器职责**：不做依赖校验、不做启用开关分发、不做配置分发——那些问题在装配点以显式代码解决。

## 11. 组件约定

约定是**文档契约**，不是运行时机制：没有接口注册、没有反射发现，装配点直接调用组件的普通函数。

### 11.1 形态与两态

- **原生组件**：按本约定编写，原生适配配置节、observ 等能力（术语见 [CONTEXT.md](../CONTEXT.md)）。
- **适配组件**：对既有第三方库包一层薄壳使之符合约定，业务自写或由资产作者发布。
- 载体两种：随模板分发的**内置组件库**与独立版本化的**组件资产**，组织与取舍见 §12。
- 组件是普通 Go 代码，物理上无法依赖模板——复制型资产没有稳定 import 路径（ADR-0001）；依赖什么由组件自定，observ 是原生组件的
  **推荐**抽象面，不是门槛（§1）。
- 第三方库无需满足任何约定——保持原样，贴合发生在适配层（ADR-0001）。

### 11.2 生命周期签名

```go
// 组件包的规范形态
type Config struct{ /* 字段带 yaml tag */ }

func Default() Config                      // 默认值，与 New 成对导出：初始解码与热更重解码的共同基座

type Component struct{ /* ... */ }

func New(cfg Config, opts ...Option) (*Component, error)
//   构造即校验：配置不合法在此返回 error（fail-fast 点）。
//   失败即未启动，无任何需要清理的资源（半构造资源由 New 内部回收）。

func (c *Component) Start(ctx context.Context) error
//   起 goroutine 后立即返回；返回 nil 即可用。ctx 取消是停机信号之一。

func (c *Component) Stop(ctx context.Context) error
//   幂等可重入；ctx 携带预算，超时自行截断。goroutine 归组件所有，Stop 负责回收。

func (c *Component) ApplyConfig(cfg Config) error   // 可选能力
//   收敛语义：相同值必须无操作。error 仅记日志，不打死进程（组件自决忽略或告警）。
//   可能在 Start 之前被调用（收敛首调与早到变更），实现必须仅更新状态、不依赖运行时资源。

func (c *Component) Client() *someclient.Client     // 可选：类型化访问器
//   依赖传递的唯一形式：装配点把具体对象作构造参数传给下一个组件。
```

### 11.3 配置约定

- 配置是**可选能力**：无配置的组件没有 Config / Default / 配置节，装配只做 New + Add（§11.5）。
- 组件**独自**定义其配置节的结构、默认值与解析；节名以包级常量 `SectionName` 自述
  （建议包名或知名缩写——文档声明的节名升格为代码事实源）。
- **统一节名接口**（可选能力，结构化、零 import）：组件实现 `Section() string`（恒返回
  `SectionName`）即自述其节名；装配点引用常量接线，`AddComponent` 校验接线节名与自述
  一致，漂移即装配失败（fail-fast）。运行时拉取节配置的句柄见 §8.3 的 `Section`。
- `Default()` 必须导出，与 `New` 成对——默认值在组件的公开签名面上，是**初始解码与热更重解码共同的基座**
  （缺失键回落默认、节消失回落全默认，都由它兜底）；配合 `config.Dump` 渲染为可粘贴 yaml，手动复制进项目 `config.yaml`：

  ```go
  // 任意一次性程序或测试中
  _ = config.Dump(os.Stdout, "redis", redis.Default())
  ```

- **启用开关不是保留键**：组件需要启停语义时，在自身 Config 定义 `Enabled bool`（缺省 true）自行处理。
- 时长类字段建议"整数 + 单位后缀"命名（如 `interval_seconds`），避免 yaml 时长反序列化歧义。

### 11.4 并发纪律

原生组件以 observ 接入规范为纪律基线：option 注入（`WithLogger` / `WithMeter` 未注入时构造期回落 observ 包级默认
`DefaultLogger` / `DefaultMeter`——快照语义，未安装即 Noop；**在 setupSources 构造的源角色组件例外——日志动态读
而非快照**，其构造早于日志装配；Meter 侧的回落依赖出口组件先接线，见 §9）；回调在调用方 goroutine 同步执行且必须快速返回；回调
panic 由组件 recover；热路径只做指标埋点，日志仅用于低频生命周期事件。第三方组件的并发行为由其自管，不在约定范围内。

### 11.5 装配形态（模板侧）

业务组件的装配集中在**业务装配入口** `internal/app/biz.go`（`setupBiz`，Run
直接调用）——业务逻辑的定位点，业务代码与模板机制（AddComponent / lifecycle / setupSources）由此分家。模板内置占位业务
`biz.Hello`（启动输出一句日志）演示完整接入链路，项目落地后替换 `internal/biz` 包即可。引入组件 =
装配点一行（装配辅助）或三行手写，二者等价；辅助是糖，不是唯一路径：

```go
// internal/app/biz.go —— 业务装配入口（Run 第 5 步直接调用）
func setupBiz(t *config.Tree, r *runner, meta Meta) error {
    // 辅助式：Default 与 New 在 AddComponent 签名上成对出现，默认值只写一处。
    // newFn 形参是 func(Cfg) (C, error)：New 不带 option 的组件可直传；
    // 带约定的 opts ...Option 时传闭包（greeter 带 WithLogger，故用闭包）：
    g, err := AddComponent(t, r, greeter.SectionName, greeter.Default(),
        func(c greeter.Config) (*greeter.Greeter, error) { return greeter.New(c) })
    if err != nil {
        return err
    }

    // 手写式（等价展开，需要完全定制时用）：
    // cfg, err := config.Decode(t, greeter.SectionName, greeter.Default())
    // g, err := greeter.New(cfg)
    // r.Add(greeter.SectionName, g.Start, g.Stop)
    // config.Watch(t, greeter.SectionName, greeter.Default(), g.ApplyConfig) // 无热更能力则省略

    // 无配置组件：直接注册
    // w := worker.New()
    // r.Add("worker", w.Start, w.Stop)
    return nil
}
```

`AddComponent`（模板自有代码，约 20 行）：

```go
// internal/app
type lifecycle interface {
    Start(context.Context) error
    Stop(context.Context) error
}

type applier[Cfg any] interface{ ApplyConfig(Cfg) error }

type sectioner interface{ Section() string } // 统一节名接口（可选，恒返回组件的 SectionName）

// AddComponent：解码（基座 def）→ newFn 构造 → 注册生命周期；
// 组件实现 ApplyConfig(Cfg) 时自动订阅节热更（重解码仍以 def 为基座）；
// 实现 Section() string 时校验自述节名与接线一致（漂移即装配失败）。
// 接口由 Go 结构化类型满足——组件零 import 即被识别。
func AddComponent[Cfg any, C lifecycle](t *config.Tree, r *runner,
    section string, def Cfg, newFn func(Cfg) (C, error)) (C, error) {
    cfg, err := config.Decode(t, section, def)
    if err != nil {
        var zero C
        return zero, err
    }
    c, err := newFn(cfg)
    if err != nil {
        var zero C
        return zero, err
    }
    if s, ok := any(c).(sectioner); ok && s.Section() != section {
        var zero C
        return zero, fmt.Errorf("app: section mismatch: wired %q, component declares %q",
            section, s.Section())
    }
    r.Add(section, c.Start, c.Stop)
    if a, ok := any(c).(applier[Cfg]); ok {
        config.Watch(t, section, def, a.ApplyConfig)
    }
    return c, nil
}
```

组件间依赖在装配点显式传参（`AddComponent` 的返回值直接喂给下一个组件）：

```go
rdb, err := AddComponent(t, r, "redis", redis.Default(),
    func(c redis.Config) (*redis.Redis, error) { return redis.New(c) })
biz, err := biz.New(biz.Config{…}, biz.WithRedis(rdb.Client())) // 方向显式：业务 → 基础组件
r.Add("biz", biz.Start, biz.Stop)
```

### 11.6 README 必含项（资产准入标准）

1. 安装方式（`go get` 路径）与 Go 版本要求
2. **可粘贴的配置节示例**（含全部字段）；无配置组件明确声明「无配置」
3. 字段表：名称 / 类型 / 缺省 / **热更列**（哪些字段变更会被 `ApplyConfig` 生效）
4. 生命周期 API 表（New / Start / Stop / 可选 ApplyConfig / 可选访问器）
5. 依赖声明：是否依赖 observ；适配组件需声明被包装库及版本
6. 并发与回调纪律的符合性说明

## 12. 资产库组织

- **内置组件库**：`internal/components/<name>`，随模板分发的组件菜单，每个组件独立一个包，依赖不设限（与普通第三方组件同待遇）。组件的依赖随组件进入模板
  go.mod；复制方取舍自由——需要的直接 import 或拷出改造，不需要的整目录删除后 `go mod tidy` 依赖即清零（先移除对应接线行）。与
  ADR-0001 的边界：被拒绝的是"以复制为分发形态、需要跨项目 bugfix 传播的资产库"
  ；内置组件随模板整体分发、落地即项目自有，不存在该问题。需要独立版本化维护的组件仍走资产
  module。用法详见 [internal/components/README.md](../internal/components/README.md)。

- **资产** = 模板之外一切可复用 Go module：契约库（observ）、原生组件、适配组件。模板与资产共同构成"资产积累库"——模板是骨架，资产是积累。
- **索引**：`ASSETS.md`（与本设计文档同目录），记录：名称 / module 路径 / 类型 / 配置节 / 热更能力 / 状态。
- **准入**：满足 §11.6 README 必含项。
- **版本**：semver tag；使用方 `go get` 锁定次版本。模板不追踪资产版本，升级是项目侧决策。
- **命名**：不强制规范，以清单登记为准。
- **失效处理**：弃用资产在清单标记状态并保留行（历史可查）。

## 附录 A：示例组件 greeter

greeter（周期打印问候语）是组件约定的完整示范，源码即文档：
`internal/components/greeter`——覆盖 Config / Default / New 构造校验 /
Start / Stop（幂等）/ ApplyConfig 热更 / observ 注入（WithLogger）/ 节名自述
（SectionName 常量 + Section 统一获取接口，§11.3）全套形态，并附测试。
三种用法见 [internal/components/README.md](../internal/components/README.md)：
直接 import 试用、拷出改造为项目自有组件、或仅作编写参考。

配置节（粘贴进 `config.yaml`，或由 `config.Dump` 生成）：

```yaml
greeter:
  message: "hello"
  interval_seconds: 10
```

装配见 §11.5；热更：`message` 生效、`interval_seconds` 生效（下个周期起，
字段语义见 Config 注释）。

## 附录 B：nacos 接入参考

nacos 已内置：`internal/components/nacos`（cfg 配置中心 Source + reg 服务注册，
组件 README 含复制即用的接入代码、配置节与字段表；历史形态见 ASSETS.md 资产行）。

接入要点（详见组件 README）：

- 源触点 `setupSources`：`config.Decode` → `NewCfgClient` → `t.Attach(cc)`——
  cfg 以 Source 兼容签名直传（§8.5 结构化类型），零适配胶水
- 组件触点 `setupBiz`：`NewReg(cfg, meta.Name, port)` → `r.Add("nacos-reg", ...)`——
  实例标识传参，serviceName 传模板元数据
- 引导自身所需配置（连接参数）只来自本地层；nacos 节为启动期配置，不热更
- 不可达降级（unreachable=disable）的高信号告警由组件双通道发出
  （observ 动态读 + 直写 stderr）——cfg 角色的降级发生在引导窗口
  （setupSources 早于日志装配），单靠 observ 会被 Noop 吞掉

## 附录 C：文档纪律

- 新模板仓库文档四件：`docs/DESIGN.md`（本文）、`docs/ASSETS.md`（资产清单）、`CONTEXT.md`（术语表，从本文 §3 拆出随代码演进维护）、
  `docs/adr/`（架构决策记录）；另附仓库门面 `README.md`（quickstart）与 `LICENSE`（MIT）。
- **ADR 判据**（三者齐备才立）：难以逆转、缺上下文会令未来读者困惑、真实权衡的结果。本设计配套 ADR 六份：ADR-0001
  组件零依赖与装配点胶水、ADR-0002 复制式消费、ADR-0003 日志单一 observ 调用面、ADR-0004 可观测组件进内置库与 observ ctx
  演进、ADR-0005 httpserver 可观测聚合（单端口收编、接口化强绑定与 Link 语义）、ADR-0006 稳定桥与请求日志独立（Rebind
  协议退役、链路注入沉入适配层、caller 恒定）。设计文档本身维持定稿直叙、无中间决策；ADR 仅作决策背景补充，**不是实现依赖**（不读 ADR 亦可凭本文完成实现）。

## 附录 D：可观测组件参考

otelc、promc 与 httpserver 已内置：`internal/components/otelc`（OTel
tracing 与日志导出）、`internal/components/promc`（指标与健康检查）、
`internal/components/httpserver`（业务 HTTP Server 与可观测中间件链），
组件 README 含复制即用的接入代码、配置节与字段表。

接入要点（详见组件 README）：

- 两组件均不热更（启动期配置），资源标识（service.name / env / version）不经
  yaml——装配点从应用元数据与构建元数据传入（`WithService(meta.Name, effEnv, version.Version)`）
- otelc 的承重行为：endpoint 为空也安装 TracerProvider，trace_id 生成
  与日志关联照常工作，仅不导出；endpoint 非空才创建 OTLP 导出器
  （grpc/http，恒 insecure——TLS 与凭据不在范围）
- otelc 的日志注入经 `CtxLogAttrs` 提取器双通道：接 zapc 时装配点以
  `zapc.WithCtxAttrs(otelc.CtxLogAttrs)` 注入，zaplog 适配层在桥内部
  附加 trace_id/span_id/request_id——调用面无装饰层，caller 定位恒定
  （桥包装 kit、身份恒定，zapc 接管 `SetDefaultLogger` 一次完成，
  ADR-0006）；未接 zapc 时构造仍以装饰兜底（slog 缺省后端的链路对齐）
- otelc 的 OTLP 日志导出（logs_enabled）：经 otelzap 桥产出 zap core，
  装配点以 `zapc.WithCore` 并进 tee——**依赖 zapc 后端**（缺省 slog
  管线无此通路，维持零第三方依赖边界）；进入 zap 的每条日志出海，
  热更重建自动带上，停机顺序顺刃（zapc 后接线先停 Sync 本地 sink、
  otelc 先接线后停 flush 出海——记录在写入时已同步入队 batch）；
  出海流不受 zapc 级别门控，级别裁剪交 collector 侧
- promc 的 Meter 即 §9 所述出口：`Meter()` 返回注册到私有 registry 的
  `observ.Meter`；`New` 同时把它安装为 observ 默认 Meter（v0.3.0
  `DefaultMeter`——其后构造的业务组件构造期回落，仪器绑定不追溯，故
  promc 须先于业务组件接线；显式 `WithMeter` 注入覆盖）；在 prometheus
  默认 registry 注册的自定义 collector 不会出现在 `/metrics`
- 跨组件健康检查由装配点胶水登记（`promc.RegisterCheck`），组件间零
  import；零登记时 `/health` 恒 healthy
- promc 的暴露形态缺省是 handler 注入：`addr` 为空（缺省）不自起独立
  server，`/metrics` `/health` 由 httpserver 经 `WithProm` 挂进业务
  路由（单端口收编，ADR-0005）；需要独立端口显式配置 `promc.addr`
- httpserver 的中间件链（Recovery → CORS → 访问日志/指标 → otelhttp
  tracing → RequestID → 请求体上限）强绑定三件可观测设施：请求日志
  经 `WithAccessLogger` 注入独立记录器（装配点从 zapc 节派生独立 zapc
  实例：配置继承、path 固定同目录 `req.log`、caller 关闭、等级独立
  门控、热更跟随；未注入回落 `zap.L()`）、tracing 走 otel 全局、指标
  注册 promc 私有 registry（`observ.Meter` 无 label 维度，故直连
  prometheus 原生 API）；请求内业务日志经 `LoggerFrom(ctx)` 取预绑定
  request_id/trace_id/span_id 的记录器（zap 全局逐请求派生，归应用流；
  直调 `zap.L()` 同源但无预绑定字段）；停机三步走（readiness 摘流 →
  关 keep-alive → Shutdown 排空），热路径跳过清单不产日志与指标
- otelc 同时安装全局 W3C 传播器（TraceContext + Baggage）——otel ≥1.33
  全局传播器缺省 noop，不显式安装则 traceparent 提取静默失效
- httpserver 的链路信任判定：可信来源（RemoteAddr ∈ trusted_proxies）
  继承 traceparent 为父 span；不可信来源新建 root span 并把外部
  traceparent 转为 Link（otelhttp PublicEndpoint 原生语义）——外部
  伪造链路无法污染内部拓扑
