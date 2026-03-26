package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/agentsaegis/go-proxy/internal/config"
)

var hookCmd = &cobra.Command{
	Use:   "hook",
	Short: "Bridge for Copilot/VS Code PreToolUse hooks",
	Long:  "Reads hook input JSON from stdin, checks the command against the proxy's pre-tool-use endpoint, and outputs deny/allow to stdout.",
	RunE:  runHook,
}

func init() {
	rootCmd.AddCommand(hookCmd)
}

func runHook(_ *cobra.Command, _ []string) error {
	// Read hook input from stdin
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}

	var hookInput struct {
		ToolName string `json:"toolName"`
		ToolArgs struct {
			Command string `json:"command"`
		} `json:"toolArgs"`
	}
	if err := json.Unmarshal(input, &hookInput); err != nil {
		// Can't parse - fail open (no output = allow)
		return nil
	}

	// Only process bash/shell tools
	toolLower := strings.ToLower(hookInput.ToolName)
	if toolLower != "bash" && toolLower != "shell" && toolLower != "run_in_terminal" {
		return nil
	}

	if hookInput.ToolArgs.Command == "" {
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		cfg = config.DefaultConfig()
	}

	// POST to proxy hook endpoint
	hookURL := fmt.Sprintf("http://localhost:%d/hooks/pre-tool-use", cfg.ProxyPort)
	reqBody, _ := json.Marshal(map[string]interface{}{
		"hook_type": "PreToolUse",
		"tool_name": "Bash",
		"tool_input": map[string]string{
			"command": hookInput.ToolArgs.Command,
		},
	})

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(hookURL, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		// Proxy unreachable - fail open, track health
		stateFile := hookHealthStateFile()
		count := readFailCount(stateFile)
		count++
		writeFailCount(stateFile, count)
		if count <= 5 {
			fmt.Fprintf(os.Stderr, "Warning: AgentsAegis proxy unreachable (%d/5)\n", count)
		}
		return nil
	}
	defer resp.Body.Close()

	// Reset health state on successful contact
	resetHookHealthState()

	if resp.StatusCode == http.StatusForbidden {
		// Denied - output deny response
		output := map[string]string{
			"permissionDecision":       "deny",
			"permissionDecisionReason": "AgentsAegis security training: suspicious command blocked",
		}
		enc := json.NewEncoder(os.Stdout)
		return enc.Encode(output)
	}

	// 200 or anything else = allow (no output)
	return nil
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
