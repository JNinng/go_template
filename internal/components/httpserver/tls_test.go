package httpserver

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// genSelfSigned 生成 127.0.0.1 的临时自签证书（测试专用）。
func genSelfSigned(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	certOut := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyOut := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certFile, certOut, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyOut, 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

// TLS：cert/key 非空即单监听 HTTPS；证书文件损坏构造期 fail-fast。
func TestTLS(t *testing.T) {
	certFile, keyFile := genSelfSigned(t)

	s, _ := newTestServer(t, func(c *Config) {
		c.CertFile, c.KeyFile = certFile, keyFile
	})
	// base 是 http:// 形态，此处换 https 直连实际端口
	url := "https://" + s.Addr() + "/livez"
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // 自签测试证书
		},
	}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("https GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("https livez = %d, want 200", resp.StatusCode)
	}
	if resp.TLS == nil {
		t.Fatal("response must be over TLS")
	}

	// 证书文件不可加载：构造期拒绝（fail-fast，不拖到监听期）
	cfg := Default()
	cfg.Addr = "127.0.0.1:0"
	cfg.CertFile, cfg.KeyFile = "no-such.pem", "no-such.pem"
	if _, err := New(cfg); err == nil {
		t.Fatal("bad cert files must fail construction")
	}
}
