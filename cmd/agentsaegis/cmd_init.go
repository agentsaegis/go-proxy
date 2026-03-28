package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/agentsaegis/go-proxy/internal/client"
	"github.com/agentsaegis/go-proxy/internal/config"
)

var (
	offlineFlag  bool
	tokenFlag    string
	dashURLFlag  string
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize AgentsAegis and connect to your organization",
	RunE:  runInit,
}

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "One-command setup: configure token, shell hooks, integrations, and start proxy",
	Long:  "Alias for 'init'. Configures API token, installs shell wrapper + PreToolUse hooks, detects Claude Desktop and Copilot, and starts the proxy daemon.",
	RunE:  runInit,
}

func init() {
	for _, cmd := range []*cobra.Command{initCmd, setupCmd} {
		cmd.Flags().BoolVar(&offlineFlag, "offline", false, "Skip dashboard connection (offline mode)")
		cmd.Flags().StringVar(&tokenFlag, "token", "", "API token (skips interactive prompt)")
		cmd.Flags().StringVar(&dashURLFlag, "dashboard-url", "", "Dashboard URL (default: https://api.agentsaegis.com)")
		rootCmd.AddCommand(cmd)
	}
}

func runInit(_ *cobra.Command, _ []string) error {
	fmt.Println("AgentsAegis - Setup")
	fmt.Println()

	var dashboardURL, apiToken string

	if offlineFlag {
		dashboardURL = "https://api.agentsaegis.com"
		fmt.Println("Offline mode - skipping dashboard connection.")
	} else {
		reader := bufio.NewReader(os.Stdin)

		// Resolve dashboard URL: flag > env > interactive > default
		dashboardURL = dashURLFlag
		if dashboardURL == "" {
			dashboardURL = os.Getenv("AEGIS_DASHBOARD_URL")
		}
		if dashboardURL == "" {
			fmt.Print("Dashboard URL [https://api.agentsaegis.com]: ")
			line, err := reader.ReadString('\n')
			if err != nil {
				return fmt.Errorf("reading dashboard URL: %w", err)
			}
			dashboardURL = strings.TrimSpace(line)
		}
		if dashboardURL == "" {
			dashboardURL = "https://api.agentsaegis.com"
		}

		// Resolve API token: flag > env > interactive
		apiToken = tokenFlag
		if apiToken == "" {
			apiToken = os.Getenv("AEGIS_API_TOKEN")
		}
		if apiToken == "" {
			fmt.Print("API Token: ")
			line, err := reader.ReadString('\n')
			if err != nil {
				return fmt.Errorf("reading API token: %w", err)
			}
			apiToken = strings.TrimSpace(line)
		}
		if apiToken == "" {
			return fmt.Errorf("API token is required (use --token flag, AEGIS_API_TOKEN env, or enter interactively)")
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
	}

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

	fmt.Printf("Configuration saved to %s\n", configPath)
	fmt.Println()

	// Auto-setup shell wrapper (Claude Code CLI)
	if shellErr := runSetupShell(nil, nil); shellErr != nil {
		fmt.Printf("Shell wrapper: skipped (%v)\n", shellErr)
	}

	// Auto-setup Claude Desktop MCP server (if installed)
	if _, statErr := os.Stat("/Applications/Claude.app"); statErr == nil {
		if desktopErr := runSetupDesktop(nil, nil); desktopErr != nil {
			fmt.Printf("Claude Desktop MCP: skipped (%v)\n", desktopErr)
		}
	}

	// Auto-setup Copilot hooks + MCP (if VS Code installed)
	home, _ := os.UserHomeDir()
	vscodeExtDir := filepath.Join(home, ".vscode", "extensions")
	if entries, err := os.ReadDir(vscodeExtDir); err == nil {
		hasCopilot := false
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "github.copilot") {
				hasCopilot = true
				break
			}
		}
		if hasCopilot {
			if copilotErr := runSetupCopilot(nil, nil); copilotErr != nil {
				fmt.Printf("Copilot hooks: skipped (%v)\n", copilotErr)
			}
		}
	}

	// Start proxy daemon
	daemonFlag = true
	if startErr := runStart(nil, nil); startErr != nil {
		fmt.Printf("Proxy daemon: skipped (%v)\n", startErr)
	} else {
		// Verify health
		healthy := false
		port := 7331
		if cfg, loadErr := config.Load(); loadErr == nil {
			port = cfg.ProxyPort
		}
		healthURL := fmt.Sprintf("http://localhost:%d/__aegis/health", port)
		for range 10 {
			resp, err := http.Get(healthURL)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					healthy = true
					break
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		if healthy {
			fmt.Printf("Proxy running on port %d\n", port)
		} else {
			fmt.Println("Warning: proxy started but health check failed")
		}
	}
	daemonFlag = false

	fmt.Println()
	fmt.Println("Setup complete. Restart your terminal (or: source ~/.zshrc)")
	fmt.Println("One-line install for your team:")
	fmt.Println("  brew install agentsaegis/tap/agentsaegis && agentsaegis setup --token <TOKEN>")
	fmt.Println()

	return nil
}
