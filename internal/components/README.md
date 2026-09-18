# internal/components — 内置组件库

与模板同 module 的轻量组件，每个组件独立一个包。三种用法：

1. **直接 import 试用**：同模块内零成本，`AddComponent(t, r, "<节名>", <pkg>.Default(), ...)` 接线
2. **拷出改造**：把组件目录拷到 `internal/` 下你自己的包，改造为项目自有代码（import 路径全局替换即可）
3. **仅作参考**：每个组件都是组件约定（DESIGN §11）的完整示范

## 准入规则（硬性）

- **仅依赖 stdlib + observ**：observ 已是模板直接依赖（零新增成本）。
  原因：`go mod tidy` 收录的是**主模块全部包**的依赖——未使用的组件包
  只要有第三方依赖，每个复制体的 go.mod 都会背上它，用不用都背着。
- 重型集成（SDK 类，如 nacos/redis 客户端）**不进这里**，走独立资产
  module（修复可传播、依赖不进模板，见 [ASSETS.md](docs/ASSETS.md)）。
- 满足组件约定六件套：Config / Default / New（构造即校验）/ Start /
  Stop（幂等）/ 可选 ApplyConfig。
