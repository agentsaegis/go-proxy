package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/agentsaegis/go-proxy/internal/config"
	"github.com/agentsaegis/go-proxy/internal/daemon"
)

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Fully remove AgentsAegis from this machine",
	Long: `Removes all AgentsAegis integrations, certificates, configuration, and data.

This runs all cleanup steps in order:
  1. Stop the running proxy daemon (if any)
  2. Remove shell wrappers from .zshrc/.bashrc/.config/fish
  3. Remove Claude Code PreToolUse hook from ~/.claude/settings.json
  4. Remove Copilot hooks and MCP server from VS Code
  5. Remove VS Code proxy settings (http.proxy)
  6. Remove CA certificate from system trust store (requires sudo)
  7. Remove ~/.agentsaegis/ directory (config, CA keys, logs, traps)

After uninstall, restart your shell and VS Code to apply changes.`,
	RunE: runUninstall,
}

func init() {
	rootCmd.AddCommand(uninstallCmd)
}

func runUninstall(_ *cobra.Command, _ []string) error {
	fmt.Println("Uninstalling AgentsAegis...")
	fmt.Println()

	var warnings []string

	// 1. Stop proxy daemon
	fmt.Print("Stopping proxy... ")
	if err := stopProxy(); err != nil {
		fmt.Printf("skipped (%s)\n", err)
	} else {
		fmt.Println("done")
	}

	// 2. Remove shell wrappers
	fmt.Print("Removing shell wrappers... ")
	if err := runRemoveShell(nil, nil); err != nil {
		warnings = append(warnings, fmt.Sprintf("shell: %v", err))
	}

	// 3. Remove Copilot integration
	fmt.Print("Removing Copilot integration... ")
	if err := runRemoveCopilot(nil, nil); err != nil {
		warnings = append(warnings, fmt.Sprintf("copilot: %v", err))
	}

	// 4. Remove VS Code proxy settings
	fmt.Print("Removing VS Code proxy settings... ")
	if err := runRemoveVSCode(nil, nil); err != nil {
		warnings = append(warnings, fmt.Sprintf("vscode: %v", err))
	}

	// 5. Remove CA from system trust store
	fmt.Print("Removing CA from system trust store... ")
	if err := untrustCA(); err != nil {
		warnings = append(warnings, fmt.Sprintf("CA trust: %v", err))
	} else {
		fmt.Println("done")
	}

	// 6. Remove ~/.agentsaegis/ directory
	fmt.Print("Removing ~/.agentsaegis/... ")
	if err := removeAegisDir(); err != nil {
		warnings = append(warnings, fmt.Sprintf("data dir: %v", err))
	} else {
		fmt.Println("done")
	}

	fmt.Println()
	if len(warnings) > 0 {
		fmt.Println("Completed with warnings:")
		for _, w := range warnings {
			fmt.Printf("  - %s\n", w)
		}
	} else {
		fmt.Println("AgentsAegis fully uninstalled.")
	}
	fmt.Println("Restart your shell and VS Code to apply changes.")

	return nil
}

// stopProxy sends SIGTERM to the running daemon (if any).
func stopProxy() error {
	configDir, err := config.ConfigDir()
	if err != nil {
		return fmt.Errorf("no config dir")
	}

	pid, err := daemon.ReadPID(configDir)
	if err != nil {
		return fmt.Errorf("not running")
	}

	if !daemon.IsRunning(pid) {
		_ = daemon.RemovePID(configDir)
		return fmt.Errorf("not running (cleaned stale PID)")
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("process %d not found", pid)
	}

	if err := process.Signal(os.Kill); err != nil {
		return fmt.Errorf("kill PID %d: %v", pid, err)
	}

	_ = daemon.RemovePID(configDir)
	return nil
}

// untrustCA removes the AgentsAegis CA from the system trust store.
func untrustCA() error {
	caPath, _ := aegisCAPath()

	switch runtime.GOOS {
	case "darwin":
		removed := false
		// Try removing trust via cert file first (works if ~/.agentsaegis/ca.pem exists).
		if caPath != "" {
			if _, statErr := os.Stat(caPath); statErr == nil {
				cmd := exec.Command("sudo", "security", "remove-trusted-cert", "-d", caPath)
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				cmd.Stdin = os.Stdin
				if err := cmd.Run(); err == nil {
					removed = true
				}
			}
		}
		// Fallback: remove by certificate name from system keychain (works even if
		// ~/.agentsaegis/ was already deleted).
		if !removed {
			// Check if the cert exists in the keychain first
			check := exec.Command("security", "find-certificate", "-c", "AgentsAegis Proxy CA", "/Library/Keychains/System.keychain")
			if checkErr := check.Run(); checkErr != nil {
				fmt.Println("skipped (no CA in keychain)")
				return nil
			}
			cmd := exec.Command("sudo", "security", "delete-certificate", "-c", "AgentsAegis Proxy CA", "/Library/Keychains/System.keychain")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Stdin = os.Stdin
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("failed to remove CA from keychain (run with sudo): %w", err)
			}
		}

	case "linux":
		// Only removes the specific file we installed at a known path.
		dest := "/usr/local/share/ca-certificates/agentsaegis.crt"
		_ = exec.Command("sudo", "rm", "-f", dest).Run()
		_ = exec.Command("sudo", "update-ca-certificates", "--fresh").Run()
	}

	return nil
}

// removeAegisDir removes the ~/.agentsaegis/ directory entirely.
func removeAegisDir() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".agentsaegis")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(dir)
}
