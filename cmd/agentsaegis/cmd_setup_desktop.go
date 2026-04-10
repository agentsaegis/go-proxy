//go:build desktop

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

var setupDesktopCmd = &cobra.Command{
	Use:   "setup-desktop",
	Short: "Add AgentsAegis MCP server to Claude Desktop config",
	RunE:  runSetupDesktop,
}

var removeDesktopCmd = &cobra.Command{
	Use:   "remove-desktop",
	Short: "Remove AgentsAegis MCP server from Claude Desktop config",
	RunE:  runRemoveDesktop,
}

func init() {
	// TODO: Claude Desktop support disabled - MITM via --proxy-server not working reliably.
	// rootCmd.AddCommand(setupDesktopCmd)
	// rootCmd.AddCommand(removeDesktopCmd)
}

func claudeDesktopConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}
	switch runtime.GOOS {
	case "linux":
		return filepath.Join(home, ".config", "Claude", "claude_desktop_config.json"), nil
	default: // darwin
		return filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json"), nil
	}
}

func runSetupDesktop(_ *cobra.Command, _ []string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding executable path: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolving executable path: %w", err)
	}

	configPath, err := claudeDesktopConfigPath()
	if err != nil {
		return err
	}

	config := make(map[string]interface{})
	data, readErr := os.ReadFile(configPath)
	if readErr == nil {
		if err := json.Unmarshal(data, &config); err != nil {
			return fmt.Errorf("claude Desktop config is not valid JSON: %w", err)
		}
	}

	mcpServers, ok := config["mcpServers"].(map[string]interface{})
	if !ok {
		mcpServers = make(map[string]interface{})
	}

	mcpServers["agentsaegis"] = map[string]interface{}{
		"command": exe,
		"args":    []string{"mcp"},
	}
	config["mcpServers"] = mcpServers

	output, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, append(output, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming config: %w", err)
	}

	relConfig := configPath
	if home, herr := os.UserHomeDir(); herr == nil && strings.HasPrefix(configPath, home) {
		relConfig = "~" + configPath[len(home):]
	}
	fmt.Printf("Added AgentsAegis MCP server to %s\n", relConfig)
	fmt.Println("Restart Claude Desktop to apply the changes.")

	return nil
}

func runRemoveDesktop(_ *cobra.Command, _ []string) error {
	configPath, err := claudeDesktopConfigPath()
	if err != nil {
		return err
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Println("No Claude Desktop config found.")
		return nil
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		fmt.Println("Claude Desktop config is not valid JSON - please edit manually.")
		return nil
	}

	if mcpServers, ok := config["mcpServers"].(map[string]interface{}); ok {
		if _, exists := mcpServers["agentsaegis"]; exists {
			delete(mcpServers, "agentsaegis")
			if len(mcpServers) == 0 {
				delete(config, "mcpServers")
			}
			output, _ := json.MarshalIndent(config, "", "  ")
			_ = os.WriteFile(configPath, append(output, '\n'), 0o644)
			fmt.Println("Removed AgentsAegis MCP server from Claude Desktop config.")
			fmt.Println("Restart Claude Desktop to apply the changes.")
			return nil
		}
	}

	fmt.Println("No AgentsAegis configuration found in Claude Desktop config.")
	return nil
}
