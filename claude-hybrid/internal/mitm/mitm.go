// Package mitm provides dynamic per-domain certificate generation and caching for TLS MITM.
package mitm

import (
	"container/list"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"

	"github.com/peter-wagstaff/claude-hybrid-router/internal/config"
)

// CertCache generates and caches per-domain TLS certificates signed by a MITM CA.
type CertCache struct {
	caCert   *x509.Certificate
	caKey    *ecdsa.PrivateKey
	maxSize  int
	validity time.Duration

	mu    sync.Mutex
	cache map[string]*list.Element
	order *list.List // LRU: front = most recently used
}

type cacheEntry struct {
	hostname string
	cert     tls.Certificate
	created  time.Time
}

// NewCertCache creates a CertCache from PEM-encoded CA certificate and key.
func NewCertCache(caCertPEM, caKeyPEM []byte) (*CertCache, error) {
	certBlock, _ := pem.Decode(caCertPEM)
	if certBlock == nil {
		return nil, fmt.Errorf("failed to decode CA certificate PEM")
	}
	caCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA certificate: %w", err)
	}

	keyBlock, _ := pem.Decode(caKeyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("failed to decode CA key PEM")
	}
	rawKey, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		// Try PKCS8
		k, err2 := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
		if err2 != nil {
			return nil, fmt.Errorf("parse CA key: %w", err)
		}
		var ok bool
		rawKey, ok = k.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("CA key is not ECDSA")
		}
	}

	return &CertCache{
		caCert:   caCert,
		caKey:    rawKey,
		maxSize:  config.MitmCacheMaxSize,
		validity: time.Duration(config.MitmCertValidityHours * float64(time.Hour)),
		cache:    make(map[string]*list.Element),
		order:    list.New(),
	}, nil
}

// GetTLSConfig returns a *tls.Config with a certificate for the given hostname.
// Results are cached with LRU eviction.
func (c *CertCache) GetTLSConfig(hostname string) (*tls.Config, error) {
	c.mu.Lock()
	if el, ok := c.cache[hostname]; ok {
		entry := el.Value.(*cacheEntry)
		if time.Since(entry.created) < c.validity {
			c.order.MoveToFront(el)
			cert := entry.cert
			c.mu.Unlock()
			return &tls.Config{
				Certificates: []tls.Certificate{cert},
				MinVersion:   tls.VersionTLS13,
				NextProtos:   []string{"http/1.1"},
			}, nil
		}
		// Expired
		c.order.Remove(el)
		delete(c.cache, hostname)
	}
	c.mu.Unlock()

	cert, err := c.generateCert(hostname)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	entry := &cacheEntry{hostname: hostname, cert: cert, created: time.Now()}
	el := c.order.PushFront(entry)
	c.cache[hostname] = el
	for c.order.Len() > c.maxSize {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.cache, oldest.Value.(*cacheEntry).hostname)
	}
	c.mu.Unlock()

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"http/1.1"},
	}, nil
}

func (c *CertCache) generateCert(hostname string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: hostname},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(c.validity),
	}

	if ip := net.ParseIP(hostname); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{hostname}
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, c.caCert, &key.PublicKey, c.caKey)
	if err != nil {
		return tls.Certificate{}, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return tls.X509KeyPair(certPEM, keyPEM)
}

// GenerateCA creates a self-signed CA certificate and key, returned as PEM bytes.
func GenerateCA() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "claude-hybrid MITM CA"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		SubjectKeyId:          []byte{1}, // Simplified; fine for local use
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, nil
}
