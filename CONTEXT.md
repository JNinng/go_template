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
与模板同 module 的轻量组件（`internal/components/`，仅依赖 stdlib + observ）；
直接 import 试用，或拷出改造为项目自有组件。
_Avoid_: vendor（Go 工具链保留目录名，包不可导入）、把重型集成放进内置库（依赖会进每个复制体的 go.mod）

**原生组件**：
遵循 [DESIGN.md](./docs/DESIGN.md) §11 约定编写的组件，原生适配配置节、observ 等能力。

**适配组件**：
包装既有第三方库使之符合约定的薄层，业务自写或资产作者发布。

**观察契约（observ）**：
零依赖的 Logger/Meter 接口契约库：模板的日志/指标调用面，原生组件的推荐抽象面。
_Avoid_: 日志后端

## 运行时结构

**装配入口（setup）**：
把远程源或组件接入进程的统一形态，全部以 `setup` 命名：`setupSources`（远程源，先于日志装配）、`setupLogging`、`setupBiz`（业务组件）。
_Avoid_: wire（不易理解的行话）、注册表、容器（均不存在）、vendor 内置包（集成型资产也是组件，走资产 module）

**业务装配入口（biz）**：
业务组件的装配定位点（`internal/app/biz.go` 的 `setupBiz`，Run 直接调用，组件经 `AddComponent` 接线）；内置占位业务组件（`internal/biz`，项目落地后替换）。
_Avoid_: 到 `Run` 时序里找业务挂载点

**运行器（runner）**：
顺序启动、逆序停止、信号、停机预算（`internal/runner` 独立机制包）。
_Avoid_: 容器（不做依赖校验、配置分发、启用开关）

**应用元数据**：
`app:` 节（name/env）+ 构建期注入的 Version。

## 配置

**配置节**：
yaml 顶层归属于某一组件的子树，由该组件独自定义与解析。

**多环境文件**：
叠加在基础配置之上的本地文件 `config.<env>.yaml`。
_Avoid_: env 文件（不是环境变量清单）

**配置源（Source）**：
向配置树推送全量快照的远程配置提供方，如配置中心。
_Avoid_: 注册中心（指服务发现设施）

**静态覆盖层**：
`from_env` 环境变量绑定与 flag 覆盖，进程内不变、无变更流。
