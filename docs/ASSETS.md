# 资产清单

> 模板与组件资产共同构成"资产积累库"：模板是骨架，资产是积累。
> 准入标准与组件约定见 [DESIGN.md](./DESIGN.md) §11 / §12。

| 资产 | module 路径 | 类型 | 配置节 | 热更 | 状态 | 说明 |
|---|---|---|---|---|---|---|
| observ | `github.com/jninng/observ` | 契约库 | — | — | 可用 | 零依赖 Logger/Meter 契约 + slog 桥 + Noop 回落 |
| observ/adapters/zaplog | `github.com/jninng/observ/adapters/zaplog` | 适配器 | — | — | 可用 | zap 实现 observ.Logger |
| observ/adapters/prom | `github.com/jninng/observ/adapters/prom` | 适配器 | — | — | 可用 | client_golang 实现 observ.Meter |
| nacos | [`github.com/jninng/nacos`](https://github.com/JNinng/nacos) | 原生 | `nacos`（config/registrar 子节分立） | 启动期配置（不热更） | 可用 | cfg 配置源（Source 签名直传 Attach）+ reg 服务注册（标准生命周期），v0.1.0（go 1.23），接入见 DESIGN.md 附录 B |

## 维护规则

- 新资产入列前须满足 DESIGN.md §11.6 README 必含项
- 版本走 semver tag；登记最低 Go 版本
- 弃用资产标记状态并保留行（历史可查）
