# internal/components — 内置组件库

随模板分发的组件菜单，每个组件独立一个包，**依赖不设限**（与普通第三方组件同待遇）。
组件的第三方依赖随组件进入模板 go.mod；复制方按需取舍：

- **需要的**：直接 import 接线（同模块零成本，`AddComponent(t, r, "<节名>", <pkg>.Default(), ...)`）；
  想深度改造就拷到 `internal/` 下你自己的包（import 路径全局替换即可）
- **不需要的**：整目录删除 → `go mod tidy`，其依赖即被清掉。
  注意先移除 `setupBiz` 里对应的接线行，否则编译不过

每个组件都是组件约定（DESIGN §11）的完整示范，可直接当编写参考。

## 约定

- 每组件独立包；满足组件约定六件套：Config / Default / New（构造即校验）/
  Start / Stop（幂等）/ 可选 ApplyConfig
- 组件包文档**声明自己的第三方依赖**——删除它对 go.mod 的影响一目了然
- 测试必须离线可跑（不依赖外部服务；对端不可达用拒连地址测，参考 nacos 资产）

## 与资产 module 的关系

内置组件随模板整体分发、落地即项目自有，不存在跨项目 bugfix 传播问题；
需要独立版本化维护、跨项目复用的组件（如 observ 契约库、nacos 客户端）
发布为资产 module（见 [ASSETS.md](../../docs/ASSETS.md)），经 `go get` 引入。
