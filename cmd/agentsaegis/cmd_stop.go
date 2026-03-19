package main

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/agentsaegis/go-proxy/internal/config"
	"github.com/agentsaegis/go-proxy/internal/daemon"
)

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the AgentsAegis daemon",
	RunE:  runStop,
}

func init() {
	rootCmd.AddCommand(stopCmd)
}

func runStop(_ *cobra.Command, _ []string) error {
	configDir, err := config.ConfigDir()
	if err != nil {
		return fmt.Errorf("getting config directory: %w", err)
	}

	pid, err := daemon.ReadPID(configDir)
	if err != nil {
		return fmt.Errorf("no running proxy found: %w", err)
	}

	if !daemon.IsRunning(pid) {
		// Process is gone - clean up PID file
		if removeErr := daemon.RemovePID(configDir); removeErr != nil {
			return fmt.Errorf("removing stale PID file: %w", removeErr)
		}
		fmt.Println("AgentsAegis proxy was not running (cleaned up stale PID file)")
		return nil
	}

	// Send SIGTERM
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("finding process %d: %w", pid, err)
	}

	if err := process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("sending SIGTERM to PID %d: %w", pid, err)
	}

	fmt.Printf("Sent SIGTERM to PID %d, waiting for exit...\n", pid)

	// Wait up to 10 seconds for the process to exit
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !daemon.IsRunning(pid) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	if daemon.IsRunning(pid) {
		fmt.Printf("Process %d did not exit within timeout, sending SIGKILL\n", pid)
		if err := process.Signal(syscall.SIGKILL); err != nil {
			return fmt.Errorf("sending SIGKILL to PID %d: %w", pid, err)
		}
	}

	// Clean up PID file
	if removeErr := daemon.RemovePID(configDir); removeErr != nil {
		// Not fatal - the process is stopped
		fmt.Printf("Warning: failed to remove PID file: %v\n", removeErr)
	}

	fmt.Println("AgentsAegis proxy stopped")
	return nil
}
