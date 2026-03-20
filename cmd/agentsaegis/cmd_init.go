package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/agentsaegis/go-proxy/internal/client"
	"github.com/agentsaegis/go-proxy/internal/config"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize AgentsAegis and connect to your organization",
	RunE:  runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
}

func runInit(_ *cobra.Command, _ []string) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("AgentsAegis - Setup")
	fmt.Println()

	// Prompt for dashboard URL
	fmt.Print("Dashboard URL [https://api.agentsaegis.com]: ")
	dashboardURL, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading dashboard URL: %w", err)
	}
	dashboardURL = strings.TrimSpace(dashboardURL)
	if dashboardURL == "" {
		dashboardURL = "https://api.agentsaegis.com"
	}

	// Prompt for API token
	fmt.Print("API Token: ")
	apiToken, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading API token: %w", err)
	}
	apiToken = strings.TrimSpace(apiToken)
	if apiToken == "" {
		return fmt.Errorf("API token is required")
	}

	// Validate the token
	fmt.Print("Validating token... ")
	apiClient := client.New(dashboardURL, apiToken)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if validateErr := apiClient.ValidateToken(ctx); validateErr != nil {
		fmt.Println("FAILED")
		return fmt.Errorf("token validation failed: %w", validateErr)
	}
	fmt.Println("OK")

	// Ensure config directory exists
	if err := config.EnsureConfigDir(); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	configDir, err := config.ConfigDir()
	if err != nil {
		return fmt.Errorf("getting config directory: %w", err)
	}

	// Write config file
	configPath := filepath.Join(configDir, "config.yaml")
	cfg := map[string]interface{}{
		"dashboard_url":     dashboardURL,
		"api_token":         apiToken,
		"proxy_port":        7331,
		"anthropic_base_url": "https://api.anthropic.com",
		"log_level":         "info",
	}
	configContent, marshalErr := yaml.Marshal(cfg)
	if marshalErr != nil {
		return fmt.Errorf("marshaling config: %w", marshalErr)
	}

	if err := os.WriteFile(configPath, configContent, 0o600); err != nil {
		return fmt.Errorf("writing config file: %w", err)
	}

	fmt.Println()
	fmt.Printf("Configuration saved to %s\n", configPath)
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1. Run: agentsaegis start")
	fmt.Println("  2. Run: agentsaegis setup-shell")
	fmt.Println("  3. Restart your terminal or source your profile")
	fmt.Println()

	return nil
}
