package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

var reloadCmd = &cobra.Command{
	Use:   "reload",
	Short: "Reload proxy configuration without restart",
	Long:  "Sends SIGHUP to the running proxy daemon to reload config.yaml and dashboard settings.",
	RunE:  runReload,
}

func init() {
	rootCmd.AddCommand(reloadCmd)
}

func runReload(_ *cobra.Command, _ []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home directory: %w", err)
	}
	pidFile := filepath.Join(home, ".agentsaegis", "proxy.pid")
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return fmt.Errorf("no running proxy found (PID file not found)")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return fmt.Errorf("invalid PID file: %w", err)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("process %d not found: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGHUP); err != nil {
		return fmt.Errorf("sending SIGHUP to %d: %w", pid, err)
	}
	fmt.Printf("Sent reload signal to proxy (PID %d).\n", pid)
	return nil
}
