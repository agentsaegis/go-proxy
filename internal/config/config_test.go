package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.DashboardURL != "https://api.agentsaegis.com" {
		t.Errorf("DashboardURL = %q, want %q", cfg.DashboardURL, "https://api.agentsaegis.com")
	}
	if cfg.ProxyPort != 7331 {
		t.Errorf("ProxyPort = %d, want %d", cfg.ProxyPort, 7331)
	}
	if cfg.AnthropicBaseURL != "https://api.anthropic.com" {
		t.Errorf("AnthropicBaseURL = %q, want %q", cfg.AnthropicBaseURL, "https://api.anthropic.com")
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
	if cfg.APIToken != "" {
		t.Errorf("APIToken = %q, want empty", cfg.APIToken)
	}
	if cfg.DeveloperID != "" {
		t.Errorf("DeveloperID = %q, want empty", cfg.DeveloperID)
	}
	if cfg.OrgID != "" {
		t.Errorf("OrgID = %q, want empty", cfg.OrgID)
	}
}

func TestLoad_DefaultsOnly(t *testing.T) {
	// Reset viper state before the test so previous test runs do not leak
	viper.Reset()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.ProxyPort != 7331 {
		t.Errorf("ProxyPort = %d, want %d", cfg.ProxyPort, 7331)
	}
	if cfg.AnthropicBaseURL != "https://api.anthropic.com" {
		t.Errorf("AnthropicBaseURL = %q, want %q", cfg.AnthropicBaseURL, "https://api.anthropic.com")
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
}

func TestLoad_FromConfigFile(t *testing.T) {
	viper.Reset()

	// Use a temp directory as a fake HOME
	tmpHome := t.TempDir()
	configDir := filepath.Join(tmpHome, ".agentsaegis")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	configContent := []byte(`dashboard_url: "https://custom.example.com"
api_token: "tok_test123"
proxy_port: 9999
anthropic_base_url: "https://custom-anthropic.example.com"
developer_id: "dev_abc"
org_id: "org_xyz"
log_level: "debug"
`)
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), configContent, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Override HOME so UserHomeDir returns our temp dir
	t.Setenv("HOME", tmpHome)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.DashboardURL != "https://custom.example.com" {
		t.Errorf("DashboardURL = %q, want %q", cfg.DashboardURL, "https://custom.example.com")
	}
	if cfg.APIToken != "tok_test123" {
		t.Errorf("APIToken = %q, want %q", cfg.APIToken, "tok_test123")
	}
	if cfg.ProxyPort != 9999 {
		t.Errorf("ProxyPort = %d, want %d", cfg.ProxyPort, 9999)
	}
	if cfg.AnthropicBaseURL != "https://custom-anthropic.example.com" {
		t.Errorf("AnthropicBaseURL = %q, want %q", cfg.AnthropicBaseURL, "https://custom-anthropic.example.com")
	}
	if cfg.DeveloperID != "dev_abc" {
		t.Errorf("DeveloperID = %q, want %q", cfg.DeveloperID, "dev_abc")
	}
	if cfg.OrgID != "org_xyz" {
		t.Errorf("OrgID = %q, want %q", cfg.OrgID, "org_xyz")
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "debug")
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	viper.Reset()

	// Viper's AutomaticEnv maps AEGIS_PROXY_PORT to proxy_port automatically
	t.Setenv("AEGIS_PROXY_PORT", "8888")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.ProxyPort != 8888 {
		t.Errorf("ProxyPort = %d, want %d", cfg.ProxyPort, 8888)
	}
}

func TestConfigDir(t *testing.T) {
	dir, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir() error = %v", err)
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("ConfigDir() = %q, want absolute path", dir)
	}
	if filepath.Base(dir) != ".agentsaegis" {
		t.Errorf("ConfigDir() basename = %q, want %q", filepath.Base(dir), ".agentsaegis")
	}
}

func TestEnsureConfigDir(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	err := EnsureConfigDir()
	if err != nil {
		t.Fatalf("EnsureConfigDir() error = %v", err)
	}

	expected := filepath.Join(tmpHome, ".agentsaegis")
	info, err := os.Stat(expected)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", expected, err)
	}
	if !info.IsDir() {
		t.Errorf("expected %q to be a directory", expected)
	}
}

func TestEnsureConfigDir_Idempotent(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := EnsureConfigDir(); err != nil {
		t.Fatalf("first EnsureConfigDir() error = %v", err)
	}
	if err := EnsureConfigDir(); err != nil {
		t.Fatalf("second EnsureConfigDir() error = %v", err)
	}
}
