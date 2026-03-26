package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

var setupCopilotCmd = &cobra.Command{
	Use:   "setup-copilot",
	Short: "Configure VS Code Copilot agent mode to use AgentsAegis",
	Long:  "Registers AgentsAegis hooks and MCP server for GitHub Copilot in VS Code. Creates hook config and updates VS Code settings.",
	RunE:  runSetupCopilot,
}

var removeCopilotCmd = &cobra.Command{
	Use:   "remove-copilot",
	Short: "Remove AgentsAegis from VS Code Copilot configuration",
	RunE:  runRemoveCopilot,
}

func init() {
	rootCmd.AddCommand(setupCopilotCmd)
	rootCmd.AddCommand(removeCopilotCmd)
}

// copilotHooksDir returns the directory for Copilot hook configs.
func copilotHooksDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}
	return filepath.Join(home, ".copilot", "hooks"), nil
}

// vscodeSettingsPath returns the path to VS Code user settings.
func vscodeSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}
	switch runtime.GOOS {
	case "linux":
		return filepath.Join(home, ".config", "Code", "User", "settings.json"), nil
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(appData, "Code", "User", "settings.json"), nil
	default: // darwin
		return filepath.Join(home, "Library", "Application Support", "Code", "User", "settings.json"), nil
	}
}

func runSetupCopilot(_ *cobra.Command, _ []string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding executable path: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolving executable path: %w", err)
	}

	// Copilot traps work via HTTPS_PROXY + TLS MITM (set by the shell wrapper).
	// No hook config needed - traps are injected in the SSE stream, same as Claude Code.

	// 1. Add MCP server to VS Code settings
	settingsPath, err := vscodeSettingsPath()
	if err != nil {
		return err
	}

	if err := addMCPToVSCode(settingsPath, exe); err != nil {
		fmt.Printf("VS Code MCP: skipped (%v)\n", err)
	} else {
		relSettings := settingsPath
		if home, _ := os.UserHomeDir(); strings.HasPrefix(settingsPath, home) {
			relSettings = "~" + settingsPath[len(home):]
		}
		fmt.Printf("Added AgentsAegis MCP server to %s\n", relSettings)
	}

	fmt.Println("Restart VS Code to apply the changes.")

	// Print trust-cert hint
	home, _ := os.UserHomeDir()
	caPath := filepath.Join(home, ".agentsaegis", "ca.pem")
	if _, err := os.Stat(caPath); err == nil {
		fmt.Println()
		fmt.Println("Run 'sudo agentsaegis trust-cert' to trust the proxy CA (required for HTTPS interception).")
	} else {
		fmt.Println()
		fmt.Println("The proxy CA will be generated on first start. Run 'sudo agentsaegis trust-cert' afterward.")
	}

	return nil
}

func addMCPToVSCode(settingsPath, exe string) error {
	settings := make(map[string]interface{})
	data, readErr := os.ReadFile(settingsPath)
	if readErr == nil {
		// VS Code settings may have comments (JSONC) - try parsing, skip if it fails
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("VS Code settings contains comments/JSONC which cannot be safely modified programmatically")
		}
	}

	// Get or create mcp.servers
	mcpSection, ok := settings["mcp"].(map[string]interface{})
	if !ok {
		mcpSection = make(map[string]interface{})
	}
	servers, ok := mcpSection["servers"].(map[string]interface{})
	if !ok {
		servers = make(map[string]interface{})
	}

	servers["agentsaegis"] = map[string]interface{}{
		"command": exe,
		"args":    []string{"mcp"},
	}
	mcpSection["servers"] = servers
	settings["mcp"] = mcpSection

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

func runRemoveCopilot(_ *cobra.Command, _ []string) error {
	removed := false

	// 1. Remove hook config
	hooksDir, err := copilotHooksDir()
	if err == nil {
		hookPath := filepath.Join(hooksDir, "agentsaegis.json")
		if err := os.Remove(hookPath); err == nil {
			fmt.Println("Removed AgentsAegis hook config.")
			removed = true
		}
	}

	// 2. Remove MCP server from VS Code settings
	settingsPath, err := vscodeSettingsPath()
	if err == nil {
		data, readErr := os.ReadFile(settingsPath)
		if readErr == nil {
			var settings map[string]interface{}
			if json.Unmarshal(data, &settings) == nil {
				if mcpSection, ok := settings["mcp"].(map[string]interface{}); ok {
					if servers, ok := mcpSection["servers"].(map[string]interface{}); ok {
						if _, exists := servers["agentsaegis"]; exists {
							delete(servers, "agentsaegis")
							if len(servers) == 0 {
								delete(mcpSection, "servers")
							}
							if len(mcpSection) == 0 {
								delete(settings, "mcp")
							}
							output, _ := json.MarshalIndent(settings, "", "  ")
							_ = os.WriteFile(settingsPath, append(output, '\n'), 0o644)
							fmt.Println("Removed AgentsAegis MCP server from VS Code settings.")
							removed = true
						}
					}
				}
			}
		}
	}

	if !removed {
		fmt.Println("No AgentsAegis configuration found for Copilot.")
	} else {
		fmt.Println("Restart VS Code to apply the changes.")
	}

	return nil
}
