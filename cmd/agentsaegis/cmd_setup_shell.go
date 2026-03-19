package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/agentsaegis/go-proxy/internal/config"
)

var setupShellCmd = &cobra.Command{
	Use:   "setup-shell",
	Short: "Configure shell environment for AgentsAegis proxy",
	RunE:  runSetupShell,
}

func init() {
	rootCmd.AddCommand(setupShellCmd)
}

func runSetupShell(_ *cobra.Command, _ []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home directory: %w", err)
	}

	shell := os.Getenv("SHELL")
	exportLine := fmt.Sprintf("export ANTHROPIC_BASE_URL=http://localhost:%d", cfg.ProxyPort)

	var profilePath string
	var sourceLine string

	switch {
	case strings.Contains(shell, "zsh"):
		profilePath = filepath.Join(homeDir, ".zshrc")
		sourceLine = "source ~/.zshrc"
	case strings.Contains(shell, "bash"):
		profilePath = filepath.Join(homeDir, ".bashrc")
		sourceLine = "source ~/.bashrc"
	case strings.Contains(shell, "fish"):
		profilePath = filepath.Join(homeDir, ".config", "fish", "config.fish")
		exportLine = fmt.Sprintf("set -gx ANTHROPIC_BASE_URL http://localhost:%d", cfg.ProxyPort)
		sourceLine = "source ~/.config/fish/config.fish"
	default:
		fmt.Printf("Unrecognized shell: %s\n", shell)
		fmt.Printf("Add this to your shell profile manually:\n\n  %s\n\n", exportLine)
		return nil
	}

	// Check if already configured
	existing, readErr := os.ReadFile(profilePath)
	if readErr == nil && strings.Contains(string(existing), "ANTHROPIC_BASE_URL") {
		fmt.Println("ANTHROPIC_BASE_URL is already set in your shell profile.")
		fmt.Println()

		// Check if the port matches
		expectedFragment := fmt.Sprintf("localhost:%d", cfg.ProxyPort)
		if !strings.Contains(string(existing), expectedFragment) {
			fmt.Println("Warning: the configured port may not match your current config.")
			fmt.Printf("Expected: %s\n", exportLine)
			fmt.Println("Check your profile and update manually if needed.")
		} else {
			fmt.Println("Configuration looks correct. No changes needed.")
		}
		return nil
	}

	// Append the export line
	f, err := os.OpenFile(profilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening %s: %w", profilePath, err)
	}

	comment := "\n# AgentsAegis proxy - route Claude Code through security proxy\n"
	if _, err := f.WriteString(comment + exportLine + "\n"); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing to %s: %w", profilePath, err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", profilePath, err)
	}

	fmt.Printf("Added to %s:\n", profilePath)
	fmt.Printf("  %s\n", exportLine)
	fmt.Println()
	fmt.Printf("Run this to apply now:\n  %s\n", sourceLine)
	fmt.Println()
	fmt.Println("Or restart your terminal.")

	return nil
}
