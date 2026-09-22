// Package instance 解析服务实例 ID（响应头 X-Instance-IDs 的构成单元）。
// 组件 internal 子包：公共出口是根包的 httpserver.InstanceID。
package instance

import "os"

// ID 解析服务实例 ID：
//
//   - INSTANCE_ID 环境变量非空 → 原样使用（调用方保证值含服务名）；
//   - 否则 {service}:{HOSTNAME}——HOSTNAME 由 k8s 注入 pod 名，拼上服务名
//     满足"实例 ID 含服务名"；service 为空时退化为裸 hostname；
//   - HOSTNAME 也缺失 → 尾段 unknown。
//
// 同一值应经 otelc.WithInstanceID 进 OTel resource（service.instance.id），
// 使 trace、日志与响应头定位到同一实例——本函数是唯一事实源。
func ID(service string) string {
	if v := os.Getenv("INSTANCE_ID"); v != "" {
		return v
	}
	host := os.Getenv("HOSTNAME")
	if host == "" {
		host = "unknown"
	}
	if service == "" {
		return host
	}
	return service + ":" + host
}
