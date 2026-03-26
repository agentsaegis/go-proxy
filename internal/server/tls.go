package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CAManager manages the proxy's root CA certificate and generates per-host
// TLS certificates for MITM interception of HTTPS CONNECT tunnels.
type CAManager struct {
	baseDir string
	caCert  *x509.Certificate
	caKey   *ecdsa.PrivateKey
	cache   sync.Map // keyed by hostname -> *tls.Certificate
}

// NewCAManager creates a CAManager that stores CA files under baseDir.
func NewCAManager(baseDir string) *CAManager {
	return &CAManager{baseDir: baseDir}
}

// EnsureCA loads the CA from disk if it exists, or generates a new one and
// saves it. The cert is written to <baseDir>/ca.pem and the key to
// <baseDir>/ca-key.pem (permissions 0600).
func (m *CAManager) EnsureCA() error {
	certPath := filepath.Join(m.baseDir, "ca.pem")
	keyPath := filepath.Join(m.baseDir, "ca-key.pem")

	certExists := fileExists(certPath)
	keyExists := fileExists(keyPath)

	if certExists && keyExists {
		return m.loadCA(certPath, keyPath)
	}

	return m.generateCA(certPath, keyPath)
}

// CACertPath returns the path to the CA certificate PEM file.
func (m *CAManager) CACertPath() string {
	return filepath.Join(m.baseDir, "ca.pem")
}

func (m *CAManager) loadCA(certPath, keyPath string) error {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("read CA cert: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("read CA key: %w", err)
	}

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("parse CA key pair: %w", err)
	}

	m.caCert, err = x509.ParseCertificate(tlsCert.Certificate[0])
	if err != nil {
		return fmt.Errorf("parse CA cert: %w", err)
	}

	ecKey, ok := tlsCert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return fmt.Errorf("CA key is not ECDSA")
	}
	m.caKey = ecKey
	return nil
}

func (m *CAManager) generateCA(certPath, keyPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate CA key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return fmt.Errorf("generate serial: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "AgentsAegis Proxy CA",
			Organization: []string{"AgentsAegis"},
		},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create CA cert: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return fmt.Errorf("parse generated CA cert: %w", err)
	}

	if err := m.writePEM(certPath, "CERTIFICATE", certDER, 0644); err != nil {
		return fmt.Errorf("write CA cert: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal CA key: %w", err)
	}
	if err := m.writePEM(keyPath, "EC PRIVATE KEY", keyDER, 0600); err != nil {
		return fmt.Errorf("write CA key: %w", err)
	}

	m.caCert = cert
	m.caKey = key
	return nil
}

// GenerateHostCert returns a TLS certificate for hostname signed by the CA.
// Results are cached in-memory keyed by hostname.
func (m *CAManager) GenerateHostCert(hostname string) (*tls.Certificate, error) {
	if v, ok := m.cache.Load(hostname); ok {
		return v.(*tls.Certificate), nil
	}

	if m.caCert == nil || m.caKey == nil {
		return nil, fmt.Errorf("CA not initialised; call EnsureCA first")
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate host key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: hostname,
		},
		DNSNames:    []string{hostname},
		NotBefore:   now.Add(-time.Minute),
		NotAfter:    now.Add(365 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, m.caCert, &key.PublicKey, m.caKey)
	if err != nil {
		return nil, fmt.Errorf("create host cert: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal host key: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("assemble host TLS cert: %w", err)
	}

	m.cache.Store(hostname, &tlsCert)
	return &tlsCert, nil
}

// GetTLSConfig returns a *tls.Config with a certificate for hostname, suitable
// for use in tls.Server().
func (m *CAManager) GetTLSConfig(hostname string) (*tls.Config, error) {
	cert, err := m.GenerateHostCert(hostname)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{*cert},
		MinVersion:   tls.VersionTLS12,
		// Force HTTP/1.1 only: prevent ALPN from advertising h2.
		// Clients like Copilot CLI will negotiate HTTP/2 if offered,
		// but our MITM tunnel uses http.ReadRequest() which only
		// handles HTTP/1.1. Without this, the second request on a
		// multiplexed HTTP/2 connection causes a GOAWAY error.
		NextProtos: []string{"http/1.1"},
	}, nil
}

func (m *CAManager) writePEM(path, blockType string, der []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: blockType, Bytes: der})
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func randomSerial() (*big.Int, error) {
	max := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, max)
}
