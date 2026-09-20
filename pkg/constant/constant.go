// Package constant 收敛跨包复用的原子常量（时间布局、格式串等）；仅当
// 两个以上包需要同一取值时才收进来，单包自用就地定义。
package constant

// RFC3339Milli 是毫秒精度的 RFC3339 时间布局；时区随渲染侧（日志取本地
// 时间）。
const RFC3339Milli = "2006-01-02T15:04:05.000Z07:00"
