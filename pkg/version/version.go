// Package version 收敛构建期注入的版本元数据，供 version 子命令、启动行
// 与可观测资源标识（service.version）共用——单一事实源，不进配置文件。
// 五字段缺省值即无注入的源码自述（dev 构建、无提交信息、未知日期）。
//
// 注入示例（date 为源码提交日期，build_time 为二进制产出时刻）：
//
//	go build -ldflags "\
//	  -X '<module>/pkg/version.Version=v1.2.3' \
//	  -X '<module>/pkg/version.Commit=$(git rev-parse --short HEAD)' \
//	  -X '<module>/pkg/version.Date=$(git log -1 --format=%cs)' \
//	  -X '<module>/pkg/version.BuildTime=$(date +%FT%T%z)'" ./cmd/app
package version

import (
	"fmt"
	"runtime"
)

var (
	Version   = "dev"             // 版本号（git tag / 发布号）
	Commit    = "none"            // 构建所用提交（git rev-parse --short HEAD）
	Date      = "unknown"         // 提交日期（git log -1 --format=%cs）：源码何时定稿
	BuildTime = "unknown"         // 构建时刻（date +%FT%T%z）：二进制何时产出
	GoVersion = runtime.Version() // 编译工具链版本（二进制自述，无需注入）
)

// String 渲染全部字段（key: value 对齐逐行），version 子命令输出用。
func String() string {
	return fmt.Sprintf("version:    %s\ncommit:     %s\ndate:       %s\nbuild_time: %s\ngo_version: %s",
		Version, Commit, Date, BuildTime, GoVersion)
}
