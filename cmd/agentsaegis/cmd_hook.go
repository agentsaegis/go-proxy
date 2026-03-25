package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

var hookCmd = &cobra.Command{
	Use:   "hook",
	Short: "Bridge for Copilot/VS Code PreToolUse hooks",
	RunE: func(_ *cobra.Command, _ []string) error {
		return fmt.Errorf("not implemented on this branch")
	},
}

func init() {
	rootCmd.AddCommand(hookCmd)
}

func hookHealthStateFile() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agentsaegis", "hook_health_failures")
}

func readFailCount(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return n
}

func writeFailCount(path string, count int) {
	_ = os.WriteFile(path, []byte(fmt.Sprintf("%d", count)), 0o644)
}

func resetHookHealthState() {
	_ = os.Remove(hookHealthStateFile())
}
