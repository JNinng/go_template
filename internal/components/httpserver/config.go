package httpserver

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"go_template/internal/components/httpserver/internal/trust"
)

// Duration 是配置里的时长字段类型：yaml 写法 "10s" / "1m30s"
// （time.ParseDuration 语法），也接受裸整数（按秒）。零值（"0s"）表示
// 不设置——直传 net/http 的语义即无超时。Go 侧取值：
// time.Duration(cfg.ReadTimeout)。
type Duration time.Duration

// MarshalYAML 渲染为字符串形态（config.Dump 生成样例用）。
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// UnmarshalYAML 接受裸整数（秒）或字符串（ParseDuration 语法）。
// 整数优先：yaml 的 int 标量也能解成字符串（"30"），先走整数分支
// 才能区分"30 秒"与缺单位的非法字符串。
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var sec int64
	if err := node.Decode(&sec); err == nil {
		*d = Duration(time.Duration(sec) * time.Second)
		return nil
	}
	var s string
	if err := node.Decode(&s); err != nil {
		return fmt.Errorf("httpserver: duration must be a string like \"10s\" or integer seconds, got %q", node.Value)
	}
	v, perr := time.ParseDuration(s)
	if perr != nil {
		return fmt.Errorf("httpserver: duration %q: %w", s, perr)
	}
	*d = Duration(v)
	return nil
}

// CORSConfig 是跨域子节（rs/cors 的配置面）。
type CORSConfig struct {
	AllowedOrigins   []string `yaml:"allowed_origins"`   // 允许的 Origin；缺省 ["*"]（内网模板定位，公网部署须收紧）
	AllowedMethods   []string `yaml:"allowed_methods"`   // 允许的方法；缺省常用全集
	AllowedHeaders   []string `yaml:"allowed_headers"`   // 允许的请求头；缺省 ["*"]（放行全部）
	AllowCredentials bool     `yaml:"allow_credentials"` // 携带凭据；缺省 false；true 与 "*" origins 互斥（构造期拒绝）
	MaxAgeSeconds    int      `yaml:"max_age_seconds"`   // preflight 缓存秒数；缺省 7200；0 = 不下发该头
}

// SectionName 是 httpserver 的配置节名（与包名一致；装配点引用本常量
// 接线，AddComponent 校验与自述一致）。
const SectionName = "httpserver"

// Config 是 httpserver 配置节（节名 SectionName）。热更支持：max_body_size、
// trusted_proxies、drain_aware、cors.*（原子替换）；其余字段变更记
// httpserver_config_restart_required 警告后不生效（监听器与路由表在
// Start 前冻结），重启生效。
type Config struct {
	Addr             string     `yaml:"addr"`               // 监听地址；缺省 :8080
	ReadTimeout      Duration   `yaml:"read_timeout"`       // 读超时（header+body 全程）；缺省 10s；0 = 无
	WriteTimeout     Duration   `yaml:"write_timeout"`      // 写超时（含慢客户端下载）；缺省 30s；0 = 无
	IdleTimeout      Duration   `yaml:"idle_timeout"`       // keep-alive 空闲超时；缺省 120s；0 = 无
	MaxHeaderBytes   int        `yaml:"max_header_bytes"`   // 请求头上限（字节）；缺省 1MB；0 = net/http 缺省
	MaxBodySize      int64      `yaml:"max_body_size"`      // 请求体上限（字节）；缺省 10MB；0 = 不限制；超限 413
	TrustedProxies   []string   `yaml:"trusted_proxies"`    // 可信代理网段（CIDR 或裸 IP）；用于 XFF 剥离与链路信任判定
	Liveness         string     `yaml:"liveness"`           // 存活探针路径；缺省 /livez
	Readiness        string     `yaml:"readiness"`          // 就绪探针路径；缺省 /readyz（停机摘流信号也走这里）
	Versions         string     `yaml:"versions"`           // 版本信息路径；缺省 /version
	CertFile         string     `yaml:"cert_file"`          // TLS 证书路径；空 = HTTP；非空须与 key_file 成对
	KeyFile          string     `yaml:"key_file"`           // TLS 私钥路径
	Pprof            bool       `yaml:"pprof"`              // 暴露 /debug/pprof/*；缺省 true（内网模板，公网部署自行关闭）
	DrainAware       bool       `yaml:"drain_aware"`        // 排空期感知停机（readiness 摘流 + Connection: close）；缺省 true
	ExtraInstanceIDs []string   `yaml:"extra_instance_ids"` // 追加的实例 ID（多实例/网关注入场景），逗号拼进 X-Instance-IDs
	CORS             CORSConfig `yaml:"cors"`               // 跨域子节
}

// Default 返回默认值基座。
func Default() Config {
	return Config{
		Addr:           ":8080",
		ReadTimeout:    Duration(10 * time.Second),
		WriteTimeout:   Duration(30 * time.Second),
		IdleTimeout:    Duration(120 * time.Second),
		MaxHeaderBytes: 1 << 20,
		MaxBodySize:    10 << 20,
		TrustedProxies: []string{
			"127.0.0.0/8", "::1/128",
			"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
		},
		Liveness:   "/livez",
		Readiness:  "/readyz",
		Versions:   "/version",
		Pprof:      true,
		DrainAware: true,
		CORS: CORSConfig{
			AllowedOrigins: []string{"*"},
			AllowedMethods: []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			AllowedHeaders: []string{"*"},
			MaxAgeSeconds:  7200,
		},
	}
}

// Validate 构造期拒绝标准：地址非空、时长非负、上限非负、路径合法且
// 互异、TLS 文件成对、代理网段可解析、CORS 凭据与通配互斥。
func (c Config) Validate() error {
	if c.Addr == "" {
		return fmt.Errorf("httpserver: addr must not be empty")
	}
	for _, d := range []struct {
		name string
		val  Duration
	}{
		{"read_timeout", c.ReadTimeout},
		{"write_timeout", c.WriteTimeout},
		{"idle_timeout", c.IdleTimeout},
	} {
		if d.val < 0 {
			return fmt.Errorf("httpserver: %s must be >= 0, got %s", d.name, time.Duration(d.val))
		}
	}
	if c.MaxHeaderBytes < 0 {
		return fmt.Errorf("httpserver: max_header_bytes must be >= 0, got %d", c.MaxHeaderBytes)
	}
	if c.MaxBodySize < 0 {
		return fmt.Errorf("httpserver: max_body_size must be >= 0, got %d", c.MaxBodySize)
	}
	paths := []struct{ name, val string }{
		{"liveness", c.Liveness},
		{"readiness", c.Readiness},
		{"versions", c.Versions},
	}
	for _, p := range paths {
		if p.val == "" {
			return fmt.Errorf("httpserver: %s must not be empty", p.name)
		}
		if !strings.HasPrefix(p.val, "/") {
			return fmt.Errorf("httpserver: %s must start with %q, got %q", p.name, "/", p.val)
		}
	}
	for i := 0; i < len(paths); i++ {
		for j := i + 1; j < len(paths); j++ {
			if paths[i].val == paths[j].val {
				return fmt.Errorf("httpserver: %s and %s must differ, both %q",
					paths[i].name, paths[j].name, paths[i].val)
			}
		}
	}
	if (c.CertFile == "") != (c.KeyFile == "") {
		return fmt.Errorf("httpserver: cert_file and key_file must be set together (cert=%q key=%q)", c.CertFile, c.KeyFile)
	}
	if _, err := trust.NewTable(c.TrustedProxies); err != nil {
		return err
	}
	if c.CORS.MaxAgeSeconds < 0 {
		return fmt.Errorf("httpserver: cors.max_age_seconds must be >= 0, got %d", c.CORS.MaxAgeSeconds)
	}
	if c.CORS.AllowCredentials {
		for _, o := range c.CORS.AllowedOrigins {
			if o == "*" {
				return fmt.Errorf("httpserver: cors.allow_credentials must not combine with wildcard allowed_origins (CORS 规范禁止)")
			}
		}
	}
	return nil
}
