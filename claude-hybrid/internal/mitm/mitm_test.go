package mitm

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"testing"
)

func mustGenerateCA(t *testing.T) ([]byte, []byte) {
	t.Helper()
	certPEM, keyPEM, err := GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	return certPEM, keyPEM
}

func TestGenerateCA(t *testing.T) {
	certPEM, _ := mustGenerateCA(t)
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("failed to decode cert PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	if !cert.IsCA {
		t.Error("cert is not CA")
	}
	if cert.Subject.CommonName != "claude-hybrid MITM CA" {
		t.Errorf("unexpected CN: %s", cert.Subject.CommonName)
	}
}

func TestCertCacheDNS(t *testing.T) {
	certPEM, keyPEM := mustGenerateCA(t)
	cache, err := NewCertCache(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("NewCertCache: %v", err)
	}

	cfg, err := cache.GetTLSConfig("example.com")
	if err != nil {
		t.Fatalf("GetTLSConfig: %v", err)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("expected 1 cert, got %d", len(cfg.Certificates))
	}

	leaf, err := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	if leaf.Subject.CommonName != "example.com" {
		t.Errorf("unexpected CN: %s", leaf.Subject.CommonName)
	}
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "example.com" {
		t.Errorf("unexpected DNS SANs: %v", leaf.DNSNames)
	}
	if len(leaf.IPAddresses) != 0 {
		t.Errorf("unexpected IP SANs: %v", leaf.IPAddresses)
	}
}

func TestCertCacheIP(t *testing.T) {
	certPEM, keyPEM := mustGenerateCA(t)
	cache, err := NewCertCache(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("NewCertCache: %v", err)
	}

	cfg, err := cache.GetTLSConfig("127.0.0.1")
	if err != nil {
		t.Fatalf("GetTLSConfig: %v", err)
	}

	leaf, err := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	if len(leaf.IPAddresses) != 1 || !leaf.IPAddresses[0].Equal(net.ParseIP("127.0.0.1")) {
		t.Errorf("unexpected IP SANs: %v", leaf.IPAddresses)
	}
	if len(leaf.DNSNames) != 0 {
		t.Errorf("unexpected DNS SANs: %v", leaf.DNSNames)
	}
}

func TestCertCacheReuse(t *testing.T) {
	certPEM, keyPEM := mustGenerateCA(t)
	cache, err := NewCertCache(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("NewCertCache: %v", err)
	}

	cfg1, _ := cache.GetTLSConfig("example.com")
	cfg2, _ := cache.GetTLSConfig("example.com")

	// Same underlying cert should be returned (cache hit)
	leaf1, _ := x509.ParseCertificate(cfg1.Certificates[0].Certificate[0])
	leaf2, _ := x509.ParseCertificate(cfg2.Certificates[0].Certificate[0])
	if leaf1.SerialNumber.Cmp(leaf2.SerialNumber) != 0 {
		t.Error("cache did not return same cert for same hostname")
	}
}

func TestCertCacheEviction(t *testing.T) {
	certPEM, keyPEM := mustGenerateCA(t)
	cache, err := NewCertCache(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("NewCertCache: %v", err)
	}
	cache.maxSize = 2

	cache.GetTLSConfig("a.com")
	cache.GetTLSConfig("b.com")
	cache.GetTLSConfig("c.com") // should evict a.com

	cache.mu.Lock()
	_, hasA := cache.cache["a.com"]
	_, hasB := cache.cache["b.com"]
	_, hasC := cache.cache["c.com"]
	cache.mu.Unlock()

	if hasA {
		t.Error("a.com should have been evicted")
	}
	if !hasB || !hasC {
		t.Error("b.com and c.com should still be cached")
	}
}

func TestCertCacheTLSVersion(t *testing.T) {
	certPEM, keyPEM := mustGenerateCA(t)
	cache, err := NewCertCache(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("NewCertCache: %v", err)
	}

	cfg, _ := cache.GetTLSConfig("example.com")
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("expected TLS 1.3, got %d", cfg.MinVersion)
	}
	if len(cfg.NextProtos) != 1 || cfg.NextProtos[0] != "http/1.1" {
		t.Errorf("unexpected ALPN: %v", cfg.NextProtos)
	}
}
