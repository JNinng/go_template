# go_template

Go 长驻服务的项目模板：命令、应用元数据、配置（两层文件 + from_env 绑定 + 热更）、日志、优雅停机。其余一切能力以**组件资产**（独立 Go module）按需引入，模板自身不捆绑任何组件。

## 快速开始（从模板落地新项目）

1. 复制本仓库全部内容（不含 `.git`）到新项目目录
2. 改 `go.mod` 的 module 名（如 `github.com/you/your-service`），全局替换 import 路径
3. `go build ./... && go test ./...`
4. 按需引入组件资产：`go get` + 在装配触点接线（远程源 → `internal/app/wire.go` 的 `wireSource`；业务组件 → **业务入口 `internal/app/biz.go`**，`app.Use` 或等价手写展开）+ 粘贴配置节
5. `go run ./cmd/app`，Ctrl+C 验证优雅退出（退出码 0）

## 命令

| 命令 | 说明 |
|---|---|
| `run`（root 默认，无参数即执行） | 完整启动时序，阻塞至信号；`--config` / `--env` / `--log-level` |
| `version` | 打印版本（构建期 `-ldflags` 注入，缺省 `dev`） |

## 配置速览

- 基础文件 `configs/config.yaml`（`--config` 可改）；`--env prod` 选定 `config.prod.yaml` 叠加
- 合并分层：代码默认值 < 基础文件 < 多环境文件 < 远程源 < `from_env` 绑定 < flag 覆盖
- `log.level` 改文件即热更生效；`from_env` 在节内声明"配置键 ← 环境变量"（重启生效）
- 退出码：优雅停机 0；构造/启动失败、停机超预算、第二信号、引导失败均为 1

完整契约（配置 API、组件约定、资产准入）见 [docs/DESIGN.md](docs/DESIGN.md)。

## 文档

- [docs/DESIGN.md](docs/DESIGN.md) — 设计全文，实现与组件编写的依据
- [CONTEXT.md](CONTEXT.md) — 术语表（单一事实源）
- [docs/ASSETS.md](docs/ASSETS.md) — 组件资产清单
- [docs/adr/](docs/adr/) — 架构决策记录
