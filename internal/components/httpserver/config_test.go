package httpserver

import (
	"bytes"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.Addr != ":8080" || c.Liveness != "/livez" || c.Readiness != "/readyz" || c.Versions != "/version" {
		t.Fatalf("Default basic = %+v", c)
	}
	if time.Duration(c.ReadTimeout) != 10*time.Second || time.Duration(c.WriteTimeout) != 30*time.Second ||
		time.Duration(c.IdleTimeout) != 120*time.Second {
		t.Fatalf("Default timeouts = %+v", c)
	}
	if c.MaxHeaderBytes != 1<<20 || c.MaxBodySize != 10<<20 {
		t.Fatalf("Default limits = %+v", c)
	}
	if len(c.TrustedProxies) != 5 || !c.Pprof || !c.DrainAware {
		t.Fatalf("Default misc = %+v", c)
	}
	if len(c.CORS.AllowedOrigins) != 1 || c.CORS.AllowedOrigins[0] != "*" || c.CORS.MaxAgeSeconds != 7200 {
		t.Fatalf("Default cors = %+v", c.CORS)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"ok default", func(*Config) {}, false},
		{"empty addr", func(c *Config) { c.Addr = "" }, true},
		{"negative read timeout", func(c *Config) { c.ReadTimeout = Duration(-time.Second) }, true},
		{"negative write timeout", func(c *Config) { c.WriteTimeout = Duration(-1) }, true},
		{"negative max header", func(c *Config) { c.MaxHeaderBytes = -1 }, true},
		{"negative max body", func(c *Config) { c.MaxBodySize = -1 }, true},
		{"zero timeouts allowed", func(c *Config) { c.ReadTimeout, c.WriteTimeout, c.IdleTimeout = 0, 0, 0 }, false},
		{"liveness without slash", func(c *Config) { c.Liveness = "livez" }, true},
		{"readiness empty", func(c *Config) { c.Readiness = "" }, true},
		{"paths collide", func(c *Config) { c.Readiness = c.Liveness }, true},
		{"cert without key", func(c *Config) { c.CertFile = "x.pem" }, true},
		{"bad proxy entry", func(c *Config) { c.TrustedProxies = []string{"10.0.0.0/8", "not-an-ip"} }, true},
		{"empty proxies allowed", func(c *Config) { c.TrustedProxies = nil }, false},
		{"bare ip proxies allowed", func(c *Config) { c.TrustedProxies = []string{"10.1.2.3"} }, false},
		{"negative max age", func(c *Config) { c.CORS.MaxAgeSeconds = -1 }, true},
		{"credentials with wildcard", func(c *Config) { c.CORS.AllowCredentials = true }, true},
		{"credentials with explicit origins", func(c *Config) {
			c.CORS.AllowCredentials = true
			c.CORS.AllowedOrigins = []string{"https://a.example"}
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				if _, err := New(cfg); err == nil {
					t.Fatal("New must reject invalid config")
				}
			}
		})
	}
}

// Duration 的 yaml 编解码：字符串（ParseDuration 语法）、裸整数（秒）、
// 非法值拒绝；MarshalYAML 渲染回字符串（config.Dump 用）。
func TestDurationYAML(t *testing.T) {
	type holder struct {
		D Duration `yaml:"d"`
	}
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{`d: 10s`, 10 * time.Second, false},
		{`d: 1m30s`, 90 * time.Second, false},
		{`d: 0s`, 0, false},
		{`d: 30`, 30 * time.Second, false},
		{`d: ten`, 0, true},
		{`d: true`, 0, true},
	}
	for _, tc := range cases {
		var h holder
		err := yaml.Unmarshal([]byte(tc.in), &h)
		if (err != nil) != tc.wantErr {
			t.Fatalf("Unmarshal(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
		}
		if err == nil && time.Duration(h.D) != tc.want {
			t.Fatalf("Unmarshal(%q) = %s, want %s", tc.in, time.Duration(h.D), tc.want)
		}
	}
	out, err := yaml.Marshal(holder{D: Duration(10 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("10s")) {
		t.Fatalf("Marshal = %q, want contains 10s", out)
	}
}

// ApplyConfig：热字段生效、冷字段拒绝（警告 + 维持原值）、非法配置报错。
func TestApplyConfig(t *testing.T) {
	s, err := New(Default())
	if err != nil {
		t.Fatal(err)
	}
	hot := Default()
	hot.MaxBodySize = 1024
	hot.TrustedProxies = []string{"10.0.0.0/8"}
	hot.DrainAware = false
	hot.CORS.AllowedOrigins = []string{"https://a.example"}
	if err := s.ApplyConfig(hot); err != nil {
		t.Fatalf("hot apply: %v", err)
	}
	if s.bodyLimit.Load() != 1024 {
		t.Fatalf("max_body_size not applied: %d", s.bodyLimit.Load())
	}
	if s.drainAware.Load() {
		t.Fatal("drain_aware not applied")
	}

	cold := Default()
	cold.Addr = ":9999"
	cold.Pprof = false
	if err := s.ApplyConfig(cold); err != nil {
		t.Fatalf("cold apply must not error: %v", err)
	}
	if s.cfg.Addr != Default().Addr {
		t.Fatalf("cold field must stay frozen: %q", s.cfg.Addr)
	}

	bad := Default()
	bad.CORS.AllowCredentials = true // 与 "*" 互斥
	if err := s.ApplyConfig(bad); err == nil {
		t.Fatal("invalid config must be rejected")
	}
}
