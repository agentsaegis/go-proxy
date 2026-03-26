package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/agentsaegis/go-proxy/internal/config"
)

var setupVSCodeCmd = &cobra.Command{
	Use:   "setup-vscode",
	Short: "Configure VS Code to route Copilot traffic through AgentsAegis proxy",
	Long: `Configures VS Code's HTTP proxy settings to route extension traffic through
the AgentsAegis proxy. This enables trap injection for GitHub Copilot in VS Code.

The command:
  1. Generates a proxy CA certificate (if not already present)
  2. Sets http.proxy in VS Code settings.json to localhost proxy
  3. Disables http.proxyStrictSSL (our CA handles TLS)
  4. Prints guidance for optional NODE_EXTRA_CA_CERTS or trust-cert`,
	RunE: runSetupVSCode,
}

var removeVSCodeCmd = &cobra.Command{
	Use:   "remove-vscode",
	Short: "Remove AgentsAegis proxy configuration from VS Code",
	RunE:  runRemoveVSCode,
}

func init() {
	rootCmd.AddCommand(setupVSCodeCmd)
	rootCmd.AddCommand(removeVSCodeCmd)
}

func runSetupVSCode(_ *cobra.Command, _ []string) error {
	// 1. Ensure CA cert exists (reuses existing CA if present)
	caPath, err := ensureCA()
	if err != nil {
		return fmt.Errorf("ensuring CA certificate: %w", err)
	}
	fmt.Printf("CA certificate: %s\n", caPath)

	// 2. Load config for proxy port
	cfg, err := config.Load()
	if err != nil {
		cfg = config.DefaultConfig()
	}

	// 3. Detect VS Code settings path
	settingsPath, err := vscodeSettingsPath()
	if err != nil {
		return err
	}

	// 4. Add proxy settings to VS Code
	proxyURL := fmt.Sprintf("http://localhost:%d", cfg.ProxyPort)
	if err := addProxyToVSCode(settingsPath, proxyURL); err != nil {
		return fmt.Errorf("configuring VS Code: %w", err)
	}

	relSettings := settingsPath
	if home, herr := os.UserHomeDir(); herr == nil && strings.HasPrefix(settingsPath, home) {
		relSettings = "~" + settingsPath[len(home):]
	}
	fmt.Printf("Configured http.proxy in %s\n", relSettings)

	// 5. Print next steps
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1. Start the proxy:  agentsaegis start")
	fmt.Println("  2. Trust the CA:     sudo agentsaegis trust-cert")
	fmt.Println("  3. Restart VS Code")
	fmt.Println()
	fmt.Printf("Alternative to trust-cert - set in your shell profile:\n")
	fmt.Printf("  export NODE_EXTRA_CA_CERTS=%s\n", caPath)

	return nil
}

func addProxyToVSCode(settingsPath, proxyURL string) error {
	settings := make(map[string]interface{})
	data, readErr := os.ReadFile(settingsPath)
	if readErr == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("VS Code settings contains comments/JSONC which cannot be safely modified programmatically")
		}
	}

	// Warn if overwriting an existing non-AgentsAegis proxy
	if existing, ok := settings["http.proxy"].(string); ok && existing != "" {
		if !strings.Contains(existing, "localhost") {
			fmt.Printf("Warning: overwriting existing http.proxy: %s\n", existing)
		}
	}

	settings["http.proxy"] = proxyURL
	settings["http.proxyStrictSSL"] = false

	output, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling settings: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return fmt.Errorf("creating settings directory: %w", err)
	}

	tmpPath := settingsPath + ".tmp"
	if err := os.WriteFile(tmpPath, append(output, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing settings: %w", err)
	}
	if err := os.Rename(tmpPath, settingsPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming settings: %w", err)
	}

	return nil
}

func runRemoveVSCode(_ *cobra.Command, _ []string) error {
	settingsPath, err := vscodeSettingsPath()
	if err != nil {
		return err
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		fmt.Println("No VS Code settings found.")
		return nil
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		fmt.Println("VS Code settings contains JSONC - please remove http.proxy manually.")
		return nil
	}

	// Only remove http.proxy if it points to localhost (ours)
	removed := false
	if proxy, ok := settings["http.proxy"].(string); ok && strings.Contains(proxy, "localhost") {
		delete(settings, "http.proxy")
		removed = true
	}
	if _, ok := settings["http.proxyStrictSSL"]; ok && removed {
		delete(settings, "http.proxyStrictSSL")
	}

	if !removed {
		fmt.Println("No AgentsAegis proxy configuration found in VS Code settings.")
		return nil
	}

	output, _ := json.MarshalIndent(settings, "", "  ")
	_ = os.WriteFile(settingsPath, append(output, '\n'), 0o644)

	fmt.Println("Removed AgentsAegis proxy settings from VS Code.")
	fmt.Println("Restart VS Code to apply the changes.")

	return nil
}
