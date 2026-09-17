# Go 服务模板 · 设计文档

> 状态：设计定稿，无待定项。本文与仓库内 `CONTEXT.md`（术语表）共同自包含：仅凭二者即可从零实现模板与组件资产，不需要参考任何其他项目。

## 1. 定位与消费方式

**是什么**：Go 长驻服务的项目模板，覆盖五项基础能力——命令、应用元数据、配置、日志、优雅停机。其余一切功能以**组件资产**（独立 Go module，见 §11/§12）按需引入，模板自身不捆绑任何组件。

**目标业务形态**：HTTP API、gRPC、消息消费者、定时任务及其混合。一次性任务以兄弟子命令存在（§6），不使用热更与停机设施；纯 CLI 工具不是目标形态。

**不是什么**：不是框架、不是可 import 的依赖库。模板被复制后即为项目自有代码，可自由修改；模板对使用方式没有任何运行时约束（无注册、无发现、无注入机制）。

**消费方式**——从模板落地新项目：

1. 复制本仓库全部内容（不含 `.git`）到新项目目录
2. 改 `go.mod` 的 module 名（如 `github.com/you/your-service`），全局替换 import 路径
3. `go build ./... && go test ./...`
4. 按需引入组件资产：`go get` + 在装配触点接线（远程源 → `wireSource`，组件 → `wire`；§4/§11）+ 粘贴配置节
5. `go run ./cmd/app`，Ctrl+C 验证优雅退出

**依赖原则**（单向，环为零）：

```
业务项目（模板复制体） ──import──▶ 组件资产 ──import──▶ observ（可选）
        │
        └─ 模板自有代码（config / app / cmd）不 import 任何组件
```

- 组件无法依赖模板——模板是复制型资产，落地后每个项目的 module 路径都不同，依赖天然单向（项目 → 组件）
- 组件的依赖不受模板约束：组件（尤其第三方库）依赖什么由其自定，模板不设限；observ 只是原生组件的**推荐**抽象面，不是准入门槛
- 模板自有代码不 import 任何组件；远程源与组件的引入只发生在装配触点 `internal/app/wire.go`（`wireSource` / `wire`，§4、§11）

## 2. 验收标准

1. **落地 5 步**：复制 → 改 module 名 → build → run → Ctrl+C 优雅退出（退出码 0）
2. **引入组件触点**：`go get` + 装配触点一行（`app.Use`，或等价手写展开）+ 配置节粘贴（无配置组件省略）；**不修改模板任何既有文件**（`wireSource` 与 `wire` 为预留空实现，填入不算修改）
3. **依赖白名单**：模板 go.mod 第三方依赖 = cobra、gopkg.in/yaml.v3、fsnotify、observ，四件封顶
4. **热更可演示**：修改 `log.level` 保存即生效（无需重启）；引入示例组件（附录 A）后其配置节热更同样可演示
5. **无组件基线**：模板原样运行 = 打印启动行 → 静默等待信号 → 预算内干净退出
6. **fail-fast**：任一组件构造或启动失败 → 已启动者逆序停止 → 退出码 1
7. **模板自带测试**：`go test ./...` 覆盖难点单测——config 包（合并分层、from_env 收集与类型推断、严格解码、Watch 收敛/合并/取消、多环境文件名推导）、runner（顺序启动/逆序停止、Start 失败回滚、停机预算）、logging（level 热更）、`app.Use`（测试内 stub 组件）；信号触发路径仅 POSIX build-tag 测试。验收 5 步保持手动演示。

## 3. 术语表

见根目录 [`CONTEXT.md`](../CONTEXT.md)（单一事实源，随代码演进维护）。

## 4. 架构总览

```
main.go（3 行：internal/cmd.Execute()）
 └─ cobra root = run（默认命令）
     └─ app.Run（唯一启动时序）
         1. config.Load     读本地两层文件（基础 + 多环境）→ 配置树 + 文件监听 + from_env 静态层
         2. wireSource(t)   源接线触点：远程配置源以 Source 接入（Attach，首快照同步）。
                            先于日志装配——log 节与元数据初值因此含远程层（format/output
                            等非热更字段方能由远程治理）；引导自配只来自本地层（§8.2）
         3. setupLogging    设 observ 默认日志后端 + log 节 level 热更订阅
         4. meta            解析应用元数据（name / 生效 env / Version），打印启动行
         5. wire(t, r)      组件接线：逐组件 解码配置节 → 构造 → 注册生命周期 →（可选）Watch 热更
         6. r.Run()         信号 → root ctx → 顺序 Start → 阻塞等待 → 逆序 Stop（预算内）
```

| 层 | 成员 | 职责 |
|---|---|---|
| 命令层 | `internal/cmd`（cobra） | 参数解析、子命令、进程退出码 |
| 装配层 | `internal/app`（Run / runner / wireSource+wire / metadata / logging） | 启动时序、源与组件接线、优雅停机 |
| 配置层 | `internal/config` | 加载、合并、节读取、热更总线、Source 接口、Dump |
| 组件层 | 外部资产（独立 module） | 一切业务与基础能力 |

引导失败（config.Load、源接线、日志装配、wire 任一步出错）时日志可能未就绪：错误信息直写 stderr，进程退出码 1。

## 5. 目录布局

```
<module>/
├── README.md                  # 五步 quickstart + 组件引入指引（指向 docs/）
├── LICENSE                    # MIT
├── cmd/app/main.go            # 3 行：internal/cmd.Execute()
├── internal/
│   ├── app/
│   │   ├── app.go             # Run()：config → logging → meta → wire → runner 时序
│   │   ├── logging.go         # 日志装配：observ 默认后端 + level 热更（换 zap 的唯一改动点）
│   │   ├── metadata.go        # app 节 Meta + Default() + var Version
│   │   ├── runner.go          # 运行器（§10 给出全文）
│   │   └── wire.go            # 装配触点：源接线 wireSource + 组件接线 wire（模板内均为空实现）
│   ├── cmd/
│   │   ├── root.go            # run（默认命令）+ --config / --env / --log-level
│   │   └── version.go         # version
│   └── config/
│       ├── config.go          # Load / Tree / Raw / Decode / Dump
│       ├── source.go          # Source 接口 + 文件监听 + 合并管线
│       ├── overlay.go         # from_env 收集与静态覆盖
│       └── bus.go             # 节级订阅与串行分发
├── configs/config.yaml        # 仅 app: 与 log: 两节
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

- Go 版本要求：1.23+
- 模板 go.mod 第三方依赖白名单：`github.com/spf13/cobra`、`gopkg.in/yaml.v3`、`github.com/fsnotify/fsnotify`、`github.com/jninng/observ`

## 6. 命令体系

CLI 库为 cobra。命令集两个，刻意收敛：

| 命令 | 行为 |
|---|---|
| `run`（root 默认，无参数即执行） | 完整启动时序（§4），阻塞至信号，返回值决定退出码 |
| `version` | 打印 `var Version`（构建期 ldflags 注入，缺省 `dev`）；仅版本字符串一行输出，不带 name/env 前缀 |

**run 的 flag**（影响配置的唯一入口）：

| flag | 缺省 | 语义 |
|---|---|---|
| `--config` | `configs/config.yaml` | 基础配置文件路径 |
| `--env` | 读 `APP_ENV` | 选定多环境文件（§8）；显式指定而文件缺失 → fail-fast |
| `--log-level` | 无 | 静态覆盖 `log.level`，置合并栈顶（§8） |

**扩展一次性子命令**：在 `internal/cmd` 增加普通 cobra 子命令即可（如数据修复、迁移任务）。兄弟子命令自行管理生命周期与 `os.Exit`，不经过 runner、不享受热更与优雅停机。

**退出码**：

| 场景 | 码 |
|---|---|
| 信号触发、预算内完成停机 | 0 |
| 任一组件构造或 Start 失败 | 1 |
| 停机超总预算被强杀 | 1 |
| 收到第二个信号被强杀 | 1 |
| 引导期（配置/装配）失败 | 1 |

## 7. 应用元数据

- **`app:` 节**（模板自持定义，位于 `internal/app/metadata.go`）：
  - `name`（string，必填非空）：应用名。缺失或为空 → 启动 fail-fast。
  - `env`（string，可选）：运行环境**声明值**，供下游消费（启动日志、可观测资源、注册分组）。
- **生效 env 的解析顺序**：`--env` > `APP_ENV` > `app.env` 声明值 > 空。前两者同时是**唯一**有权选择多环境文件的输入（避免"配置里改 env 换文件"的循环依赖）。
- **Version**：`internal/app` 包级 `var Version = "dev"`，构建期注入：
  ```
  go build -ldflags "-X '<module>/internal/app.Version=v1.2.3'" ./cmd/app
  ```
  Version 不进配置文件。
- **启动行**：`observ.DefaultLogger().Log(slog.LevelInfo, "service_started", slog.String("app_name", …), slog.String("app_env", …), slog.String("app_version", …))`（消息与字段 snake_case，见 §9 日志规范），模板运行的最小可见信号。
- **消费方式**：元数据是纯数据。组件需要它时由装配点显式传参（如注册组件的 service name 传 `meta.Name`），不存在元数据广播机制。

## 8. 配置体系

### 8.1 文件与定位

- 基础文件：`--config` 指定，缺省 `configs/config.yaml`。缺失 → fail-fast。
- 多环境文件：生效 env 非空时，在基础文件同目录、扩展名前插 `.<env>`（`config.yaml` + `--env prod` → `config.prod.yaml`）。显式指定而文件缺失 → fail-fast。多环境文件与基础文件享有同等的热更监听。
- 文件监听实现要求：监听父目录并按路径过滤——fsnotify 直监听文件会漏掉 symlink 替换（k8s ConfigMap）与编辑器原子写（temp+rename）两类事件。
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
- 热更引发的每次树重建与重解码，都按同一分层重新合并——代码默认值始终是基座、静态层始终在栈顶，**运行时覆盖与启动时同构**（§11.5 的 `app.Use` / `config.Watch` 重解码即依赖此性质）。
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

- Source 由组件资产（如 nacos 客户端）构成，模板只认此接口；Source 的签名全部由朴素类型构成，**可由结构化类型满足**——资产零 import 模板、暴露同签名方法即可直传 `Attach`，无需适配胶水（接入示例见附录 B）。
- 不可达策略（fail / disable）是组件资产的客户端选项，不是模板机制。
- **装配纪律——先源后一切**：源接线（`wireSource`）先于日志装配与一切组件装配；配合 `Attach` 的首快照同步语义，日志初值与组件初值都总是完整的"本地 + 远程 + 静态层"合并结果，不存在"后附源靠热更收敛"的时序歧义（非热更字段如 `log.format` 也因此可由远程治理）。

### 8.6 热更总线契约

- **串行有序**：树替换与通知全局同序；订阅者最终状态恒等于 `Raw` 读到的状态。
- **逐订阅投递**：每个订阅在专属 goroutine 内串行回调，订阅之间互不阻塞；缓冲深度 1，突发变更合并为最新值。
- **收敛语义**：`Watch` 建立即以当前值首调一次；`apply` 必须幂等（相同值无操作）。
- **隔离**：apply 内 panic 被 recover 并记日志；apply 应快速返回，重活自行异步。
- **取消**：`cancel()` 返回后该订阅无在途且无后续回调。
- 初始配置永远在装配期以 `Decode` 显式读取；`Watch` 建立时的收敛首调与 `Decode` 结果一致（幂等实现下无操作），真正的变更投递只发生于其后——收敛首调同时封住 Decode 与订阅建立之间的竞态间隙。

## 9. 日志

**单一调用面**：模板代码严格经 **observ.Logger**（`Enabled(slog.Level) bool` / `Log(level, msg string, attrs ...slog.Attr)`，级别与属性复用 slog 类型）。组件不强制：原生组件推荐同走 observ 约定（§11），第三方组件按其日志面经适配层桥接（见下表）。装配点设置 observ 默认后端：缺省实现零配置可用（基于 stdlib 构建）；业务换 zap 时在装配点一处换向，业务自身代码直调 zap——不经 observ、不经任何中间层，高频路径零额外开销。

**持有规则**：

- **模板包**：不持有 logger 字段，调用点动态读 `observ.DefaultLogger()`（atomic 读，无锁；模板无高频路径，读取代价可忽略）。原因：config 包的构造早于日志装配（`log:` 节在配置里，先有配置后有后端），构造期快照会永久固定在 Noop；动态读同时保证换后端对已构造的模板设施立即生效。
- **组件资产**：不做统一要求。原生组件推荐按 observ 规范——`WithLogger(observ.Logger)` option 显式注入（测试捕获用），未注入时构造期快照 `observ.DefaultLogger()`，组件构造发生在装配点、晚于后端设置，快照即正确后端；第三方组件按其自身日志面经适配层桥接（见下表）。

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

| 字段 | 取值 | 热更 |
|---|---|---|
| `level` | debug / info / warn / error | **是**（唯一热更字段，作用于后端级别，调用面无感） |
| `format` | text / json | 否（重启生效） |
| `output` | `stdout` 或文件路径 | 否（重启生效） |

**非法 level 值**：启动期（flag 或文件）`level` 非法 → 构造失败 fail-fast；热更收到非法值 → 记 warn 保持旧值（Watch 契约：解码失败丢弃本次）。

**output 为文件**：logging 装配注册停机钩子关闭文件（stdlib handler 无缓冲，纯卫生，不丢数据）。

**不做文件轮转**：`output` 文件仅追加。轮转归属部署侧（logrotate / 容器 runtime）或业务换入的 zap 方案——这是缺省日志链路保持零第三方依赖的边界。

**原生组件如何拿到日志**：不显式传递。装配点保证 setupLogging 先于 wire，组件构造期快照 `observ.DefaultLogger()` 即正确后端；显式 `WithLogger` 注入保留给测试。模板与原生组件共享同一个包级默认，无传递机制。第三方组件不经此路径，按其日志面形态桥接（见下表）。

**第三方库的日志面**（适配组件的桥接规则，按库接口形态三选一）：

| 库的日志接口 | 接法 |
|---|---|
| 接收 `*slog.Logger` | 传 `slog.Default()`——缺省后端即它；换 zap 时可选地把 slog.Default 一并重指向（见配方末行），零额外桥 |
| 自有 logger 接口 | 适配层以 observ.Logger 实现该接口（几行委托代码） |
| 接收 zap 等具体后端 | 直接传该后端实例，不绕 observ |

**换 zap 配方**（`logging.go` 一处替换；业务代码直调 zap）：

```go
z := zap.Must(zap.NewProduction())
observ.SetDefaultLogger(zaplog.New(z))   // 模板与组件全部换向（observ/adapters/zaplog）
// 装配点补停机冲刷：
// r.Add("zap-sync", nil, func(context.Context) error { _ = z.Sync(); return nil })
// 可选：把仍走 slog.Default() 的第三方库重指向 zap（zap 生态的 slog handler）
```

业务代码直调 zap，不经任何桥接层；模板与组件经 zaplog 适配器直抵 zap，零改动（动态读与 wire 前换向都指向新后端）。level 热更由 zap 动态级别承接（如 `zapcore.NewAtomicLevel`）。

**Meter**：模板不装配指标出口——组件经 observ.Meter 埋点时缺省 Noop、零开销；业务需要真实指标时，在装配点构造 prom 适配器并经组件 option 注入（与日志同一注入规范），模板自身对 Meter 无任何装配代码。

**日志规范**（模板自有代码执行，原生组件建议同遵）：

- 消息与字段一律 snake_case。消息命名 `{模块}_{动作}_{状态}`，后缀：操作失败 `_failed`（默认）、校验/状态异常 `_error`、正常态 `_success` / `_started` / `_completed`；字段必须带业务前缀（`app_name`、`file_path`、`error`），禁 `id` / `name` / `msg` 等模糊名。
- 级别：技术故障（IO/连接/配置加载/panic）= Error 且自动附 `stack`（仅在错误最底层打一次）；业务与校验异常（热更值被拒等）= Warn；关键流程节点 = Info。配置值与敏感信息不落日志。
- 并发安全：后端换向经 observ 原子替换，级别热更经 slog LevelVar——不存在对全局 logger 的直接重赋值。

## 10. 优雅停机

运行器是模板唯一的生命周期机制，全文如下（规范级参考实现）：

```go
// internal/app/runner.go
package app

import (
    "context"
    "fmt"
    "log/slog"
    "os"
    "os/signal"
    "syscall"
    "time"
)

const (
    stepTimeout = 5 * time.Second  // 单组件停止预算
    totalBudget = 10 * time.Second // 停机总预算
)

type entry struct {
    name  string                       // 组件名（装配点注册时给定，用于日志与错误归因）
    start func(context.Context) error  // 启动钩子，nil 表示跳过
    stop  func(context.Context) error  // 停止钩子（须幂等），nil 表示跳过
}

// runner 按注册顺序启动、逆序停止所辖组件；信号与停机预算由其统一管理。
type runner struct{ entries []entry } // entries：注册序即启动序，逆序即停止序

// Add 注册一对生命周期；start / stop 均可为 nil（nil 跳过）。
// 注册顺序即启动顺序，逆序即停止顺序。
func (r *runner) Add(name string, start, stop func(context.Context) error) {
    r.entries = append(r.entries, entry{name, start, stop})
}

// Run 阻塞执行：安装信号处理 → 顺序启动 → 等待信号/取消 → 逆序停机。
// 返回 nil 即优雅退出（退出码 0）；启动失败返回错误（退出码 1）。
func (r *runner) Run() error {
    sigCh := make(chan os.Signal, 2)
    signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
    defer signal.Stop(sigCh)

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    go func() { // 第一个信号 → 优雅停机；第二个信号 → 立即强杀（防 Stop 卡死拖住进程）
        <-sigCh
        cancel()
        <-sigCh
        os.Exit(1)
    }()

    started, err := r.startAll(ctx)
    if err != nil {
        return err
    }

    <-ctx.Done()
    return r.shutdown(started)
}

// startAll 顺序启动；任一失败即逆序停止已启动者并返回错误（fail-fast 点）。
func (r *runner) startAll(ctx context.Context) (int, error) {
    started := 0
    for _, e := range r.entries {
        if e.start == nil {
            continue
        }
        if err := e.start(ctx); err != nil {
            _ = r.shutdown(started)
            return started, fmt.Errorf("start %s: %w", e.name, err)
        }
        started++
    }
    return started, nil
}

// shutdown 逆序停止前 n 个已启动组件：单步预算 stepTimeout、总预算 totalBudget。
// Stop 错误仅记日志（技术故障 Error）不中断流程；预算耗尽即强杀（退出码 1）。
func (r *runner) shutdown(n int) error {
    deadline := time.Now().Add(totalBudget)
    for i := n - 1; i >= 0; i-- {
        e := r.entries[i]
        if e.stop == nil {
            continue
        }
        step := time.Now().Add(stepTimeout)
        if step.After(deadline) {
            step = deadline
        }
        stepCtx, cancel := context.WithDeadline(context.Background(), step)
        err := e.stop(stepCtx)
        cancel()
        if err != nil {
            logError("component_stop_failed", // Error + stack（logging.go）
                slog.String("component_name", e.name), slog.Any("error", err))
        }
        if time.Now().After(deadline) && i > 0 {
            logError("shutdown_budget_exhausted",
                slog.Int("remaining_components", i))
            os.Exit(1)
        }
    }
    return nil
}
```

契约要点：

- **顺序**：注册顺序 = 启动顺序（依赖即书写顺序）；逆序 = 停止顺序。
- **Start 语义**：起 goroutine 后立即返回；返回 nil 即"可用"。
- **Start 无预算**：停机预算只承诺 Stop；Start 阻塞卡死以第二个信号强杀为唯一逃生门——有意接受的边界。
- **Stop 语义**：必须幂等（可安全重入）；ctx 携带单步预算，超时由组件自行截断返回。
- **预算**：单步 5s、总 10s，**常量写死**（需要不同预算 = 改代码，刻意不设配置面）。
- **信号**：SIGINT / SIGTERM → cancel root ctx；第二个信号立即 `os.Exit(1)`；SIGQUIT 保留 Go 默认栈转储。
- **无容器职责**：不做依赖校验、不做启用开关分发、不做配置分发——那些问题在装配点以显式代码解决。

## 11. 组件约定

约定是**文档契约**，不是运行时机制：没有接口注册、没有反射发现，装配点直接调用组件的普通函数。

### 11.1 形态与两态

- **原生组件**：按本约定编写，原生适配配置节、observ 等能力。
- **适配组件**：对既有第三方库（如 go-redis）包一层薄壳，使之符合约定；壳可由业务自写，也可由资产作者发布为适配资产。
- 组件是普通 Go module，可以放独立仓库（不同 git 组织亦可，见 §12）。约定只面向**想要紧密贴合模板的原生组件**；第三方库无需满足任何约定——它保持原样，贴合发生在适配层。组件的依赖自由：依赖什么由组件自定（物理上也无法依赖模板——复制型资产没有稳定 import 路径）；observ 是原生组件的推荐抽象面，不是门槛。
- **集成型资产（配置中心、注册中心、缓存等基础设施工客户端）也是普通组件**：与业务组件同约定、同准入（§11.6）、同登记（§12），不存在"内置 vendor 包"之类的特殊类别——集成物的复杂逻辑与测试住在资产 module 内，修复经 `go get -u` 传播；若内置进模板，复制型消费会把适配器 bug 冻结在每个项目副本里（ADR-0001 拒绝复制型资产的同一理由）。

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
- 组件**独自**定义其配置节的结构、默认值与解析；节名由组件文档声明（建议包名或知名缩写）。
- `Default()` 必须导出，与 `New` 成对——默认值在组件的公开签名面上，是**初始解码与热更重解码共同的基座**（缺失键回落默认、节消失回落全默认，都由它兜底）；配合 `config.Dump` 渲染为可粘贴 yaml，手动复制进项目 `config.yaml`：

  ```go
  // 任意一次性程序或测试中
  _ = config.Dump(os.Stdout, "redis", redis.Default())
  ```

- **启用开关不是保留键**：组件需要启停语义时，在自身 Config 定义 `Enabled bool`（缺省 true）自行处理。
- 时长类字段建议"整数 + 单位后缀"命名（如 `interval_seconds`），避免 yaml 时长反序列化歧义。

### 11.4 并发纪律

原生组件以 observ 接入规范为纪律基线：option 注入（`WithLogger` / `WithMeter`，缺省 Noop / 默认快照）；回调在调用方 goroutine 同步执行且必须快速返回；回调 panic 由组件 recover；热路径只做指标埋点，日志仅用于低频生命周期事件。第三方组件的并发行为由其自管，不在约定范围内。

### 11.5 装配形态（模板侧）

引入组件 = 装配点一行（装配辅助）或三行手写，二者等价；辅助是糖，不是唯一路径：

```go
// internal/app/wire.go —— 组件接线触点（源接线见 wireSource，先于日志装配）
func wire(t *config.Tree, r *runner) error {
    // 辅助式：Default 与 New 在 Use 签名上成对出现，默认值只写一处。
    // newFn 形参是 func(Cfg) (C, error)：New 不带 option 的组件可直传；
    // 带约定的 opts ...Option 时传闭包（greeter 带 WithLogger，故用闭包）：
    g, err := app.Use(t, r, "greeter", greeter.Default(),
        func(c greeter.Config) (*greeter.Greeter, error) { return greeter.New(c) })
    if err != nil {
        return err
    }

    // 手写式（等价展开，需要完全定制时用）：
    // cfg, err := config.Decode(t, "greeter", greeter.Default())
    // g, err := greeter.New(cfg)
    // r.Add("greeter", g.Start, g.Stop)
    // config.Watch(t, "greeter", greeter.Default(), g.ApplyConfig) // 无热更能力则省略

    // 无配置组件：直接注册
    // w := worker.New()
    // r.Add("worker", w.Start, w.Stop)
    return nil
}
```

`app.Use`（模板自有代码，约 20 行）：

```go
// internal/app
type lifecycle interface {
    Start(context.Context) error
    Stop(context.Context) error
}

type applier[Cfg any] interface{ ApplyConfig(Cfg) error }

// Use：解码（基座 def）→ newFn 构造 → 注册生命周期；
// 组件实现 ApplyConfig(Cfg) 时自动订阅节热更（重解码仍以 def 为基座）。
// 接口由 Go 结构化类型满足——组件零 import 即被识别。
func Use[Cfg any, C lifecycle](t *config.Tree, r *runner,
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
    r.Add(section, c.Start, c.Stop)
    if a, ok := any(c).(applier[Cfg]); ok {
        config.Watch(t, section, def, a.ApplyConfig)
    }
    return c, nil
}
```

组件间依赖在装配点显式传参（`Use` 的返回值直接喂给下一个组件）：

```go
rdb, err := app.Use(t, r, "redis", redis.Default(),
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

- **资产** = 模板之外一切可复用 Go module：契约库（observ）、原生组件、适配组件。模板与资产共同构成"资产积累库"——模板是骨架，资产是积累。
- **索引**：`ASSETS.md`（与本设计文档同目录），记录：名称 / module 路径 / 类型 / 配置节 / 热更能力 / 状态。
- **准入**：满足 §11.6 README 必含项。
- **版本**：semver tag；使用方 `go get` 锁定次版本。模板不追踪资产版本，升级是项目侧决策。
- **命名**：不强制规范，以清单登记为准。
- **失效处理**：弃用资产在清单标记状态并保留行（历史可查）。

## 附录 A：示例组件 greeter（约定完整演示）

不随模板分发；全文拷贝即可作为一个合格原生组件的起点。

```go
// 包 greeter：周期打印问候语，演示全部组件约定。
package greeter

import (
    "context"
    "fmt"
    "log/slog"
    "sync"
    "time"

    "github.com/jninng/observ"
)

type Config struct {
    Message        string `yaml:"message"`         // 问候内容，热更生效
    IntervalSec    int    `yaml:"interval_seconds"` // 周期（秒），热更生效（下个周期起）
}

func Default() Config {
    return Config{Message: "hello", IntervalSec: 10}
}

type Option func(*Greeter)

func WithLogger(l observ.Logger) Option {
    return func(g *Greeter) { g.logger = l }
}

type Greeter struct {
    mu     sync.Mutex
    cfg    Config
    logger observ.Logger
    stop   chan struct{}
    done   chan struct{}
}

func New(cfg Config, opts ...Option) (*Greeter, error) {
    if cfg.IntervalSec <= 0 {
        return nil, fmt.Errorf("greeter: interval_seconds must be > 0, got %d", cfg.IntervalSec)
    }
    g := &Greeter{
        cfg:    cfg,
        logger: observ.DefaultLogger(), // 构造期快照
        stop:   make(chan struct{}),
        done:   make(chan struct{}),
    }
    for _, o := range opts {
        o(g)
    }
    return g, nil
}

func (g *Greeter) Start(ctx context.Context) error {
    go g.loop(ctx)
    return nil
}

func (g *Greeter) loop(ctx context.Context) {
    defer close(g.done)
    for {
        t := time.NewTicker(time.Duration(g.current().IntervalSec) * time.Second)
        select {
        case <-ctx.Done():
            t.Stop()
            return
        case <-g.stop:
            t.Stop()
            return
        case <-t.C:
            c := g.current()
            g.logger.Log(slog.LevelInfo, "greeter tick",
                slog.String("message", c.Message))
            t.Stop()
        }
    }
}

func (g *Greeter) Stop(ctx context.Context) error {
    select { // 幂等关闸
    case <-g.stop:
    default:
        close(g.stop)
    }
    select { // 等回收，尊重预算
    case <-g.done:
        return nil
    case <-ctx.Done():
        return ctx.Err()
    }
}

func (g *Greeter) ApplyConfig(cfg Config) error {
    if cfg.IntervalSec <= 0 {
        return fmt.Errorf("greeter: reject non-positive interval_seconds")
    }
    g.mu.Lock()
    defer g.mu.Unlock()
    g.cfg = cfg
    return nil
}

func (g *Greeter) current() Config {
    g.mu.Lock()
    defer g.mu.Unlock()
    return g.cfg
}
```

配置节（粘贴进 `config.yaml`，或由 `config.Dump` 生成）：

```yaml
greeter:
  message: "hello"
  interval_seconds: 10
```

装配（见 §11.5）；README 字段表热更列：`message` 生效、`interval_seconds` 生效（下个周期起）。

## 附录 B：nacos 接入参考

nacos 是原生组件资产的接入参考（单仓库两包；状态见 [ASSETS.md](./ASSETS.md)）：

- `nacos/cfg`——配置中心客户端：连接、订阅 dataId、推送全量快照。自带 `unreachable: fail | disable` 客户端选项（fail = 启动报错；disable = 告警后以纯本地配置继续，热更停摆）。配置中心与服务中心地址、凭据**分立**（两个子节），两者常为不同集群、故障域独立。`nacos/cfg` 以 Source 兼容签名暴露（`Name` + `Start`，§8.5 结构化类型），装配侧零胶水直传 `Attach`。
- `nacos/reg`——服务注册客户端：注册、心跳、注销，标准生命周期签名，`Stop` 即注销。启用时需要 service name / port（装配点传 `meta.Name`）。

装配触点参考（Source 接口见 §8.5；cfg 接源触点、reg 接组件触点）：

```go
// wireSource：源触点——先于日志装配（§4 时序），引导自配只来自本地层
func wireSource(t *config.Tree) error { // 示意
    cfg, err := config.Decode(t, "nacos", nacos.Default())
    if err != nil {
        return err
    }
    cc, err := nacos.NewCfgClient(cfg) // Start 兼容 Source 签名（§8.5 结构化类型）
    if err != nil {
        return err // unreachable=fail 在此报错
    }
    return t.Attach(cc) // 首快照同步：返回时树已含远程层，日志/组件初值完整
}

// wire：组件触点——注册中心是普通组件，走标准生命周期
//   reg, err := nacos.NewReg(/* meta.Name, 端口, 凭据 */)
//   if err != nil { return err }
//   r.Add("nacos-reg", reg.Start, reg.Stop)
```

nacos 节为启动期配置：不实现 `ApplyConfig`，变更仅下次启动生效（配置中心自身的连接参数无法热切换）。

## 附录 C：文档纪律

- 新模板仓库文档四件：`docs/DESIGN.md`（本文）、`docs/ASSETS.md`（资产清单）、`CONTEXT.md`（术语表，从本文 §3 拆出随代码演进维护）、`docs/adr/`（架构决策记录）；另附仓库门面 `README.md`（quickstart）与 `LICENSE`（MIT）。
- **ADR 判据**（三者齐备才立）：难以逆转、缺上下文会令未来读者困惑、真实权衡的结果。本设计配套 ADR 三份：ADR-0001 组件零依赖与装配点胶水、ADR-0002 复制式消费、ADR-0003 日志单一 observ 调用面。设计文档本身维持定稿直叙、无中间决策；ADR 仅作决策背景补充，**不是实现依赖**（不读 ADR 亦可凭本文完成实现）。
