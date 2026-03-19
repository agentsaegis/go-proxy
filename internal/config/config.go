// Package config handles AgentsAegis proxy configuration loading and management.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

// Config holds all proxy configuration values.
type Config struct {
	DashboardURL     string `mapstructure:"dashboard_url"`
	APIToken         string `mapstructure:"api_token"`
	ProxyPort        int    `mapstructure:"proxy_port"`
	AnthropicBaseURL string `mapstructure:"anthropic_base_url"`
	DeveloperID      string `mapstructure:"developer_id"`
	OrgID            string `mapstructure:"org_id"`
	LogLevel         string `mapstructure:"log_level"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		DashboardURL:     "https://api.agentsaegis.com",
		ProxyPort:        7331,
		AnthropicBaseURL: "https://api.anthropic.com",
		LogLevel:         "info",
	}
}

// Load reads configuration from ~/.agentsaegis/config.yaml and environment variables.
func Load() (*Config, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting home directory: %w", err)
	}

	configDir := filepath.Join(homeDir, ".agentsaegis")

	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(configDir)
	viper.SetEnvPrefix("AEGIS")
	viper.AutomaticEnv()

	// Set defaults
	viper.SetDefault("dashboard_url", "https://api.agentsaegis.com")
	viper.SetDefault("proxy_port", 7331)
	viper.SetDefault("anthropic_base_url", "https://api.anthropic.com")
	viper.SetDefault("log_level", "info")

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("reading config: %w", err)
		}
		// Config file not found is OK -- use defaults
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}

	return &cfg, nil
}

// ConfigDir returns the path to the AgentsAegis config directory.
func ConfigDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}
	return filepath.Join(homeDir, ".agentsaegis"), nil
}

// EnsureConfigDir creates the config directory if it doesn't exist.
func EnsureConfigDir() error {
	dir, err := ConfigDir()
	if err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o700)
}
