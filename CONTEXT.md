# go_template

Go 长驻服务项目模板的统一语言。设计与实现依据见 [docs/DESIGN.md](./docs/DESIGN.md)；本表随代码演进而维护。

## 资产形态

**模板**：
复制型项目骨架，落地后即项目自有代码。
_Avoid_: 框架（不可被 import 依赖）

**组件（组件资产）**：
独立 Go module 形态的可复用能力单元，就是普通第三方库。
_Avoid_: 插件（无运行时注册/发现机制）

**内置组件（内置组件库）**：
随模板分发的组件菜单（`internal/components/`，每组件独立包，依赖不设限）；
需要的直接 import 或拷出改造，不需要的删除后 `go mod tidy` 依赖即清零。
_Avoid_: vendor（Go 工具链保留目录名，包不可导入）

**原生组件**：
遵循 [DESIGN.md](./docs/DESIGN.md) §11 约定编写的组件，原生适配配置节、observ 等能力。

**适配组件**：
包装既有第三方库使之符合约定的薄层，业务自写或资产作者发布。

**观察契约（observ）**：
零依赖的 Logger/Meter 接口契约库：模板的日志/指标调用面，原生组件的推荐抽象面。
_Avoid_: 日志后端

## 运行时结构

**装配入口（setup）**：
把远程源或组件接入进程的统一形态，全部以 `setup` 命名：`setupSources`（远程源，先于日志装配）、`setupLogging`、`setupBiz`
（业务组件）。
_Avoid_: wire（不易理解的行话）、注册表、容器（均不存在）、vendor 内置包（内置组件的家是 `internal/components/`，Go 工具链保留
vendor 目录、包不可导入）

**业务装配入口（biz）**：
业务组件的装配定位点（`internal/app/biz.go` 的 `setupBiz`，Run 直接调用，组件经 `AddComponent` 接线）；内置占位业务组件（
`internal/biz`，项目落地后替换）。
_Avoid_: 到 `Run` 时序里找业务挂载点

**运行器（runner）**：
顺序启动、逆序停止、信号、停机预算（`internal/runner` 独立机制包）。
_Avoid_: 容器（不做依赖校验、配置分发、启用开关）

**应用元数据**：
`app:` 节（name/env）+ 构建期注入的版本元数据（`pkg/version` 五字段）。

## 可观测

**链路追踪组件（otelc）**：
内置组件库的 OTel 追踪组件（`internal/components/otelc`）：全局
TracerProvider 装配 + 全局 W3C 传播器 + OTLP 导出 + 日志链路注入
（`CtxLogAttrs` 提取器：接 zapc 时经 `WithCtxAttrs` 沉入适配层，未接
zapc 时装饰兜底）。
_Avoid_: APM（指商业监控产品）

**指标与健康组件（promc）**：
内置组件库的指标健康组件（`internal/components/promc`）：私有
Prometheus registry + `/metrics` `/health` handler（缺省由 httpserver
单端口挂载，配置 addr 才自起独立 server）+ `observ.Meter` 适配。
_Avoid_: 监控面板（指 Grafana 类消费侧）、默认 registry（promc 用私有
registry，不经 `prometheus.DefaultRegisterer` 的指标不暴露）

**HTTP 服务组件（httpserver）**：
内置组件库的业务 HTTP Server 组件（`internal/components/httpserver`）：
标准库 ServeMux 路由 + 可观测中间件链（访问日志/指标/tracing/
RequestID/Recovery/CORS/请求体上限），单端口收编 promc 端点，停机
三步走（readiness 摘流 → 关 keep-alive → 排空）。
_Avoid_: Web 框架（路由是标准库 ServeMux，组件不是框架）、网关（不做路由转发）

**请求日志（request log）**：
httpserver 访问/panic 日志的独立日志流：装配点从 zapc 节派生独立
zapc 实例（配置继承、热更跟随、等级独立门控），`path` 固定 zapc.path
同目录 `req.log`、caller 关闭，经 `WithAccessLogger` 注入。请求内业务
日志不经此流——`LoggerFrom(ctx)` 由 zap 全局逐请求派生、预绑定链路
字段，业务日志归应用流，两流按排障口径分立。
_Avoid_: 业务日志混入 req.log（排障互扰、受请求等级门控）、直调
`zap.L()` 记请求内日志（无预绑定链路字段）

**实例 ID（instance ID）**：
服务实例的标识（`{服务名}:{HOSTNAME}`，INSTANCE_ID 环境变量可整体
覆盖）：响应头 `X-Instance-IDs`、OTel resource 属性 `service.instance.id`
与启动日志共用同一值（`httpserver.InstanceID` 是单一事实源）。
_Avoid_: pod 名（HOSTNAME 只是来源之一，实例 ID 还包含服务名）

**排空（drain）**：
停机时摘除流量并让在途请求完整返回的窗口：readiness 翻 503（负载
均衡摘除）+ 关 keep-alive（`Connection: close`）+ Shutdown 排空
（drain_aware 配置控制，缺省开）。
_Avoid_: 立即断连（drain 的对立面；在途请求须服务完）

## 配置

**配置节**：
yaml 顶层归属于某一组件的子树，由该组件独自定义与解析。

**节名（section name）**：
配置节的顶层键名，组件领地的边界。组件以包级常量 `SectionName` 自述、以
`Section()` 统一获取（结构化接口，零 import），装配点引用常量接线，
`AddComponent` 校验接线与自述一致（fail-fast）。
_Avoid_: 装配点字面量（漂移不受校验）

**多环境文件**：
叠加在基础配置之上的本地文件 `config.<env>.yaml`。
_Avoid_: env 文件（不是环境变量清单）

**配置源（Source）**：
向配置树推送全量快照的远程配置提供方，如配置中心。
_Avoid_: 注册中心（指服务发现设施）

**静态覆盖层**：
`from_env` 环境变量绑定与 flag 覆盖，进程内不变、无变更流。
