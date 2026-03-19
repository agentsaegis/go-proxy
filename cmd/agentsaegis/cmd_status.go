package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/agentsaegis/go-proxy/internal/config"
	"github.com/agentsaegis/go-proxy/internal/daemon"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show proxy status and session stats",
	RunE:  runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func runStatus(_ *cobra.Command, _ []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	configDir, err := config.ConfigDir()
	if err != nil {
		return fmt.Errorf("getting config directory: %w", err)
	}

	fmt.Println("AgentsAegis Proxy Status")
	fmt.Println()

	// Check if daemon is running
	pid, readErr := daemon.ReadPID(configDir)
	switch {
	case readErr != nil:
		fmt.Println("  Status:  STOPPED")
	case daemon.IsRunning(pid):
		fmt.Printf("  Status:  RUNNING (PID %d)\n", pid)
	default:
		fmt.Printf("  Status:  STOPPED (stale PID file, was PID %d)\n", pid)
		// Clean up stale PID file
		if removeErr := daemon.RemovePID(configDir); removeErr != nil {
			fmt.Printf("  Warning: failed to remove stale PID file: %v\n", removeErr)
		}
	}

	fmt.Printf("  Port:    %d\n", cfg.ProxyPort)
	fmt.Printf("  Target:  %s\n", cfg.AnthropicBaseURL)

	// Organization connection
	if cfg.APIToken != "" {
		fmt.Printf("  Org:     connected (%s)\n", cfg.DashboardURL)
	} else {
		fmt.Println("  Org:     not connected (offline mode)")
	}

	fmt.Println()

	return nil
}
