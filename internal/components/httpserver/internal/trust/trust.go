// Package trust 承载可信代理网段表与客户端真实 IP 解析。组件 internal
// 子包：表实例由根包持有并随 trusted_proxies 热更整体原子替换。
//
// 网段表是两处信任判定的单一事实源：X-Forwarded-For 剥离（ClientIP）
// 与 traceparent 继承判定（middleware 的 tracing 层）。
package trust

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Table 是已解析的可信代理网段表：不可变快照，整体原子替换（热更即换
// 新表，在途请求持旧表到请求结束）。nil 表 = 不信任任何代理。
type Table []netip.Prefix

// NewTable 解析网段清单：每项须为合法 CIDR（10.0.0.0/8）或裸 IP（视为
// 单主机网段）；空清单合法（不信任任何代理，返回 nil）。
func NewTable(entries []string) (Table, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	table := make(Table, 0, len(entries))
	for _, e := range entries {
		p, err := ParseEntry(e)
		if err != nil {
			return nil, err
		}
		table = append(table, p)
	}
	return table, nil
}

// ParseEntry 解析单项：CIDR 或裸 IP。
func ParseEntry(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("httpserver: trusted_proxies entry %q: %w", s, err)
		}
		return p.Masked(), nil
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("httpserver: trusted_proxies entry %q: %w", s, err)
	}
	p, err := addr.Prefix(addr.BitLen())
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("httpserver: trusted_proxies entry %q: %w", s, err)
	}
	return p, nil
}

// Contains 报告 ip 是否落在任一可信网段（IPv4-mapped IPv6 地址归一后
// 比较；无效地址恒不可信）。nil 表恒 false。
func (t Table) Contains(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	ip = ip.Unmap()
	for _, p := range t {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// RemoteIP 提取 r.RemoteAddr 的 IP 部分（无端口形态按整串解析）。
func RemoteIP(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

// ClientIP 解析客户端真实 IP（访问日志与业务共用口径）：
//
//   - 直连（RemoteAddr 不在可信网段）→ RemoteAddr 本身，XFF 一律无视
//     （外部伪造头在此路径被丢弃）；
//   - 经可信代理 → X-Forwarded-For 从右往左剥离可信代理，取首个不可信
//     IP；全部可信取最左合法值；畸形跳保守跳过。多个 XFF 头按 RFC 合并。
//
// 不读 X-Real-IP：与 XFF 双机制并存只会漂移，留一个口。
func ClientIP(r *http.Request, t Table) string {
	ip, ok := RemoteIP(r)
	if !ok {
		return r.RemoteAddr
	}
	if !t.Contains(ip) {
		return ip.String()
	}
	var hops []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(h, ",")...)
	}
	if len(hops) == 0 {
		return ip.String()
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			continue
		}
		if a = a.Unmap(); !t.Contains(a) {
			return a.String()
		}
	}
	for _, h := range hops { // 全部可信 → 最左合法值
		if a, err := netip.ParseAddr(strings.TrimSpace(h)); err == nil {
			return a.Unmap().String()
		}
	}
	return ip.String()
}
