# zapc — 内置组件（zap 日志）

把 `go.uber.org/zap` 装配为生命周期组件：启停装全局、配置热更（级别即时生效，
其余变更重建实例）、文件轮转（lumberjack）。包名拼 `c` 与 `go.uber.org/zap`
消解同名（约定见库 README）。与模板自持的 `log` 节（slog 链路，
`app/logging.go`）互不干涉——接了本组件即同时有两条日志链路：模板链路走
observ，本组件把实例装进 zap 全局——组件壳只管生命周期，调用面走
`zap.L()` / `zap.S()` 或 kit 的 `Info` / `Check`。

**第三方依赖**：`go.uber.org/zap` + `gopkg.in/natefinch/lumberjack.v2`（轮转）。
删除本目录并 `go mod tidy` 后即从 go.mod 清除。

## 接入（复制即用，无需 go get——组件已在本模块内）

**组件触点** `internal/app/biz.go`：

```go
logc, err := AddComponent(t, r, "zapc", zapc.Default(), zapc.New)
if err != nil {
	return err // 配置非法或输出打不开在此报错（退出码 1）
}
```

`New` 签名恰为 `func(Config) (*Log, error)`，可直接作 newFn；实现 `ApplyConfig`
即自动订阅 `zapc` 节热更（`AddComponent` 内建识别）。

**手动接线 / 纯 kit 复用**（绕开 AddComponent 时，注入订阅能力——组件包不
import 模板 config 包，节的选择留在装配点）：

```go
kit, err := zapc.NewLogger(cfg, zapc.WithWatch(
	func(apply func(zapc.Config) error) func() {
		return config.Watch(t, "zapc", zapc.Default(), apply)
	}))
// kit.Current() 始终返回当前实例；节变更自动走 kit.Apply 择路
```

**配置节**（追加到 `configs/config.yaml`，或由 `config.Dump(os.Stdout, "zapc", zapc.Default())` 生成）：

```yaml
zapc:
  level: info          # debug|info|warn|error|dpanic|panic|fatal；热更即时生效
  format: console      # 文件编码：console|json；热更重建生效
  path: ""             # 日志文件路径；空 = 不写文件；热更重建生效
  max_size: 256        # 单文件最大大小 (MB)；热更重建生效
  max_age: 60          # 保留天数；热更重建生效
  max_backups: 120     # 保留份数；热更重建生效
  compress: true       # 压缩历史日志；热更重建生效
  log_to_console: true # 输出到控制台（stderr，固定人类可读格式，不随 format 走 json）
```

缺省（零配置）为纯控制台输出；`path` 非空即同时写文件，两路可并存。

## 行为契约

- **双输出并联**：文件与控制台各自成 core（`zapcore.NewTee`），共享同一动态
  级别；控制台编码固定 console——本地时间戳 + 彩色级别（文件无色，避免
  ANSI 转义污染落盘内容），无 caller（经 kit 调用面行号无意义）
- **热更择路**：仅 `level` 变更走 `AtomicLevel.SetLevel`——实例不换、已取出
  的引用照常工作；其余任一字段变更走重建——kit 内部换实例（`Current` 跟随）、
  `zap.ReplaceGlobals` 同步全局、旧实例刷盘并即刻关句柄；动态级别跨重建
  复用（同一 `AtomicLevel`）
- **WithWatch 注入订阅**：热更状态机收敛在 kit（`LoggerKit.Apply`），两条
  接入路径共享同一语义——`AddComponent` 经 `ApplyConfig` 自动订阅；手动
  接线经 `WithWatch` 注入 `config.Watch` 能力（含建立时的首调收敛），取消
  并入 `Close`。两者并存属双订阅，收敛语义下无害但多余——二选一
- **轮转**（path 非空时，lumberjack）：按 `max_size` 切分、`max_age` 清理、
  `max_backups` 限量、`compress` 归档压缩；轮转文件名取本地时区
- **收敛与拒绝**：相同值无操作；非法值（级别、格式、轮转参数非正、两路输出
  全关）拒绝并保持旧配置，热更期失败由 `config.Watch` 告警、不影响运行
- **全局安装**：`Start` 时 `zap.ReplaceGlobals`；重建路径同步全局（先于 Start
  的收敛热更也会装上，无害）
- **停机**：`Stop` 幂等可重入——`Sync` 刷盘后释放文件句柄（控制台为空操作）；
  错误均吞（stderr Sync 在个别平台报 EINVAL 噪声，不视作停机失败）
- **fail-fast**：`path` 打不开在构造 / 热更期即报错——目录就地创建
  （`MkdirAll`），文件先探针打开（lumberjack 惰性开文件，不探针则坏路径
  拖到首次写才暴露）；zap 内部写失败默认落 stderr
- **Error 自带调用方栈**：`kit.Error` 自动附加 `stack` 字段，跳过封装帧、
  栈首帧即调用方代码行（测试断言定位）。栈仅由封装的 Error 注入——经
  `Current()` / `zap.L()` 直调不产生栈（直调深度不可知，宁缺毋错）；其余
  级别无栈无 caller（热路径零开销）
- **Check 原语义**：`kit.Check` 级别禁用时返回 nil，调用方判空后经
  `ce.Write(fields)` 落盘（延迟求值入口，任意级别可用）

## 字段速查

| 字段             | 缺省       | 说明                            |
|----------------|----------|-------------------------------|
| level          | info     | 最低级别，七档；唯一即时热更字段              |
| format         | console  | 文件编码 console / json；变更触发重建    |
| path           | ""（不写文件） | 日志文件路径（追加）；变更触发重建             |
| max_size       | 256      | 单文件最大大小 (MB)；非正值拒绝；变更触发重建     |
| max_age        | 60       | 保留天数；非正值拒绝；变更触发重建             |
| max_backups    | 120      | 保留份数；非正值拒绝；变更触发重建             |
| compress       | true     | 压缩历史日志；变更触发重建                 |
| log_to_console | true     | 控制台输出（stderr，固定非 json）；变更触发重建 |

## 生命周期 API

| API                                                                  | 说明                             |
|----------------------------------------------------------------------|--------------------------------|
| `Default() Config`                                                   | 默认值基座（与 `config.Decode` 成对使用）  |
| `(Config).Validate() error`                                          | 校验取值，构造期与热更期共用同一拒绝标准           |
| `New(cfg Config) (*Log, error)`                                      | 构造即校验并打开 sink；失败即未启动，无资源需清理    |
| `(*Log).Start(ctx) error`                                            | `zap.ReplaceGlobals` 安装全局后立即返回 |
| `(*Log).Stop(ctx) error`                                             | Sync 刷盘 + 释放句柄；幂等可重入           |
| `(*Log).ApplyConfig(Config) error`                                   | 热更入口（委托 kit.Apply）：级别即时 / 其余重建，收敛语义 |
| `Watcher func(apply func(Config) error) (cancel func())`             | 配置订阅能力（与 `config.Watch` 结构化对齐），装配点注入 |
| `WithWatch(Watcher) Option`                                          | `NewLogger` 可选项：注入订阅，取消并入 Close |
| `NewLogger(cfg Config, ...Option) (LoggerKit, error)`                | 工厂：实例与热更状态收于 kit 内部，其他自建 zap 日志的组件复用 |
| `LoggerKit` 方法：`Debug / Info / Warn / Error / DPanic / Check / Current / Apply / Rebuild / Close` | 调用面（Error 自带调用方栈；Check 级别禁用返回 nil）、当前实例（热更自动跟随）、智能热更入口、强制重建、取消订阅 + 释放句柄 |
