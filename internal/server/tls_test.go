package server

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
)

func TestCAManager_EnsureCA_GeneratesFiles(t *testing.T) {
	dir := t.TempDir()
	m := NewCAManager(dir)

	if err := m.EnsureCA(); err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}

	certPath := filepath.Join(dir, "ca.pem")
	keyPath := filepath.Join(dir, "ca-key.pem")

	if _, err := os.Stat(certPath); err != nil {
		t.Errorf("ca.pem not created: %v", err)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Errorf("ca-key.pem not created: %v", err)
	}
}

func TestCAManager_EnsureCA_KeyPermissions(t *testing.T) {
	dir := t.TempDir()
	m := NewCAManager(dir)

	if err := m.EnsureCA(); err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}

	keyPath := filepath.Join(dir, "ca-key.pem")
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat ca-key.pem: %v", err)
	}

	if info.Mode().Perm() != 0600 {
		t.Errorf("ca-key.pem permissions = %o, want 0600", info.Mode().Perm())
	}
}

func TestCAManager_EnsureCA_ValidCA(t *testing.T) {
	dir := t.TempDir()
	m := NewCAManager(dir)

	if err := m.EnsureCA(); err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}

	if m.caCert == nil {
		t.Fatal("caCert is nil after EnsureCA")
	}
	if !m.caCert.IsCA {
		t.Error("generated cert IsCA = false, want true")
	}
	if m.caCert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("missing KeyUsageCertSign")
	}
	if m.caCert.KeyUsage&x509.KeyUsageCRLSign == 0 {
		t.Error("missing KeyUsageCRLSign")
	}
}

func TestCAManager_EnsureCA_LoadsFromDisk(t *testing.T) {
	dir := t.TempDir()

	// Generate CA with first manager
	m1 := NewCAManager(dir)
	if err := m1.EnsureCA(); err != nil {
		t.Fatalf("first EnsureCA: %v", err)
	}
	originalSerial := m1.caCert.SerialNumber.String()

	// Load with second manager - should not regenerate
	m2 := NewCAManager(dir)
	if err := m2.EnsureCA(); err != nil {
		t.Fatalf("second EnsureCA: %v", err)
	}

	if m2.caCert.SerialNumber.String() != originalSerial {
		t.Error("second EnsureCA generated a new CA instead of loading from disk")
	}
}

func TestCAManager_GenerateHostCert_HasCorrectSAN(t *testing.T) {
	dir := t.TempDir()
	m := NewCAManager(dir)
	if err := m.EnsureCA(); err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}

	hostname := "api.github.com"
	cert, err := m.GenerateHostCert(hostname)
	if err != nil {
		t.Fatalf("GenerateHostCert: %v", err)
	}

	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf cert: %v", err)
	}

	found := false
	for _, san := range leaf.DNSNames {
		if san == hostname {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("SAN %q not found in %v", hostname, leaf.DNSNames)
	}
}

func TestCAManager_GenerateHostCert_SignedByCA(t *testing.T) {
	dir := t.TempDir()
	m := NewCAManager(dir)
	if err := m.EnsureCA(); err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}

	cert, err := m.GenerateHostCert("api.github.com")
	if err != nil {
		t.Fatalf("GenerateHostCert: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(m.caCert)

	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}

	opts := x509.VerifyOptions{
		Roots:     pool,
		DNSName:   "api.github.com",
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if _, err := leaf.Verify(opts); err != nil {
		t.Errorf("leaf cert verification failed: %v", err)
	}
}

func TestCAManager_GenerateHostCert_Caching(t *testing.T) {
	dir := t.TempDir()
	m := NewCAManager(dir)
	if err := m.EnsureCA(); err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}

	hostname := "api.github.com"
	cert1, err := m.GenerateHostCert(hostname)
	if err != nil {
		t.Fatalf("first GenerateHostCert: %v", err)
	}
	cert2, err := m.GenerateHostCert(hostname)
	if err != nil {
		t.Fatalf("second GenerateHostCert: %v", err)
	}

	if cert1 != cert2 {
		t.Error("second call returned different pointer - caching not working")
	}
}

func TestCAManager_GetTLSConfig(t *testing.T) {
	dir := t.TempDir()
	m := NewCAManager(dir)
	if err := m.EnsureCA(); err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}

	cfg, err := m.GetTLSConfig("api.github.com")
	if err != nil {
		t.Fatalf("GetTLSConfig: %v", err)
	}
	if len(cfg.Certificates) == 0 {
		t.Fatal("TLS config has no certificates")
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %v, want TLS 1.2", cfg.MinVersion)
	}
}
