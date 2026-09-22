package trust

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return a.Unmap()
}

func TestTable(t *testing.T) {
	if _, err := NewTable([]string{"10.0.0.0/8", "not-an-ip"}); err == nil {
		t.Fatal("invalid entry must be rejected")
	}
	table, err := NewTable([]string{"10.0.0.0/8", "192.168.1.10", "::1/128"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		ip   string
		want bool
	}{
		{"10.1.2.3", true},
		{"10.255.0.1", true},
		{"192.168.1.10", true},  // 裸 IP → 单主机网段
		{"192.168.1.11", false}, // 同段其他主机不在单主机网段内
		{"::1", true},
		{"::ffff:10.1.2.3", true}, // IPv4-mapped IPv6 归一命中
		{"8.8.8.8", false},
		{"172.16.0.1", false},
	}
	for _, tc := range cases {
		if got := table.Contains(mustAddr(t, tc.ip)); got != tc.want {
			t.Errorf("Contains(%s) = %v, want %v", tc.ip, got, tc.want)
		}
	}
	if (Table)(nil).Contains(mustAddr(t, "10.0.0.1")) {
		t.Error("nil table must trust nothing")
	}
}

// ClientIP 的 XFF 剥离语义（信任私网 + loopback 的表）。
func TestClientIP(t *testing.T) {
	table, err := NewTable([]string{"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	req := func(remote string, xff ...string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		r.RemoteAddr = remote
		for _, v := range xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		return r
	}
	cases := []struct {
		name   string
		r      *http.Request
		wantIP string
	}{
		// 直连可信网段外：XFF 一律无视（外部伪造头在此路径被丢弃）
		{"direct untrusted ignores XFF", req("8.8.8.8:1000", "1.1.1.1, 2.2.2.2"), "8.8.8.8"},
		// 可信代理转发：从右剥离可信代理，取首个不可信 IP
		{"strip trusted proxies", req("10.0.0.5:1000", "203.0.113.7, 10.0.0.9, 10.0.0.5"), "203.0.113.7"},
		// 多个 XFF 头按 RFC 合并
		{"multiple XFF headers", req("10.0.0.5:1000", "203.0.113.7, 10.0.0.9", "10.0.0.5"), "203.0.113.7"},
		// 全部可信 → 最左
		{"all trusted leftmost", req("10.0.0.5:1000", "10.0.0.1, 10.0.0.2, 10.0.0.5"), "10.0.0.1"},
		// 畸形跳保守跳过
		{"malformed hops skipped", req("10.0.0.5:1000", "garbage, 203.0.113.9, 10.0.0.5"), "203.0.113.9"},
		// 可信来源但无 XFF → RemoteAddr
		{"trusted no XFF", req("127.0.0.1:1000"), "127.0.0.1"},
		// IPv6 直连
		{"ipv6 direct", req("[2001:db8::1]:1000"), "2001:db8::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClientIP(tc.r, table); got != tc.wantIP {
				t.Fatalf("ClientIP = %q, want %q", got, tc.wantIP)
			}
		})
	}
}
