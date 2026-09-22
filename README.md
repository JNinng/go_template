# go_template

Go 长驻服务的项目模板：命令、应用元数据、配置（两层文件 + from_env 绑定 + 热更）、日志、优雅停机。其余一切能力以**组件**
形态按需引入：内置组件库（`internal/components/`，随模板分发、删留自便）与组件资产（独立 Go module）两种载体。

## 快速开始（从模板落地新项目）

1. 复制本仓库全部内容（不含 `.git`）到新项目目录
2. 改 `go.mod` 的 module 名（如 `github.com/you/your-service`），全局替换 import 路径
3. `go build ./... && go test ./...`
4. 按需引入组件：内置组件直接 import 或删留取舍（`internal/components/`）；组件资产 `go get` + 在装配入口接线（远程源 →
   `internal/app/sources.go` 的 `setupSources`；业务组件 → **业务入口 `internal/app/biz.go`**，`AddComponent` 或等价手写展开）+
   粘贴配置节
5. `go run ./cmd/app`，Ctrl+C 验证优雅退出（退出码 0）

## 命令

| 命令                    | 说明                                                |
|-----------------------|---------------------------------------------------|
| `run`（root 默认，无参数即执行） | 完整启动时序，阻塞至信号；`--config` / `--env` / `--log-level` |
| `version`             | 打印版本信息（`pkg/version` 五字段，构建期 `-ldflags` 注入）   |

## 配置速览

- 基础文件 `configs/config.yaml`（`--config` 可改）；`--env prod` 选定 `config.prod.yaml` 叠加
- 合并分层：代码默认值 < 基础文件 < 多环境文件 < 远程源 < `from_env` 绑定 < flag 覆盖
- `log.level` 改文件即热更生效；`from_env` 在节内声明"配置键 ← 环境变量"（重启生效）
- 退出码：优雅停机 0；构造/启动失败、停机超预算、第二信号、引导失败均为 1

完整契约（配置 API、组件约定、资产准入）见 [docs/DESIGN.md](docs/DESIGN.md)。

## 组件资产

模板与组件资产共同构成"资产积累库"：模板是骨架，资产是积累。清单与准入标准见 [docs/ASSETS.md](docs/ASSETS.md)。

- **内置组件**（随模板分发，复制即用）：约定示范样例 greeter、nacos 双角色客户端（配置源 + 服务注册）、zapc
  日志组件（热更日志器），取舍与接入见 [internal/components/README.md](internal/components/README.md)
- **组件资产**（独立 Go module，`go get` 引入）：清单与准入标准见 [docs/ASSETS.md](docs/ASSETS.md)

## 文档

- [docs/DESIGN.md](docs/DESIGN.md) — 设计全文，实现与组件编写的依据
- [CONTEXT.md](CONTEXT.md) — 术语表（单一事实源）
- [docs/ASSETS.md](docs/ASSETS.md) — 组件资产清单
- [docs/adr/](docs/adr/) — 架构决策记录
