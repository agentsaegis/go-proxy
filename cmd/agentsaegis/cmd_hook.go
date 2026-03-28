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
	Short: "Bridge for Claude Code and Copilot PreToolUse hooks",
	Long:  "Reads hook input JSON from stdin, checks the command against the proxy's pre-tool-use endpoint, and outputs deny/allow to stdout. Supports both Claude Code (snake_case) and Copilot (camelCase) hook formats.",
	RunE:  runHook,
}

func init() {
	rootCmd.AddCommand(hookCmd)
}

// hookInput represents the union of Claude Code and Copilot hook stdin formats.
// Claude Code uses snake_case, Copilot uses camelCase.
type hookInput struct {
	// Claude Code format (snake_case)
	SessionID     string          `json:"session_id"`
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	ToolUseID     string          `json:"tool_use_id"`

	// Copilot format (camelCase)
	ToolNameCamel string          `json:"toolName"`
	ToolArgs      json.RawMessage `json:"toolArgs"`
}

// resolvedToolName returns the tool name from whichever format was provided.
func (h *hookInput) resolvedToolName() string {
	if h.ToolName != "" {
		return h.ToolName
	}
	return h.ToolNameCamel
}

// resolvedToolInput returns the raw tool input JSON from whichever format was provided.
func (h *hookInput) resolvedToolInput() json.RawMessage {
	if len(h.ToolInput) > 0 {
		return h.ToolInput
	}
	return h.ToolArgs
}

// isCopilotFormat returns true if the input uses Copilot's camelCase format.
func (h *hookInput) isCopilotFormat() bool {
	return h.ToolNameCamel != "" && h.ToolName == ""
}

func runHook(_ *cobra.Command, _ []string) error {
	// Read hook input from stdin
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}

	var hi hookInput
	if err := json.Unmarshal(input, &hi); err != nil {
		// Can't parse - fail open (no output = allow)
		return nil
	}

	// Only process bash/shell tools
	toolLower := strings.ToLower(hi.resolvedToolName())
	if toolLower != "bash" && toolLower != "shell" && toolLower != "run_in_terminal" {
		return nil
	}

	// Extract command from tool input (supports both object and string-wrapped JSON)
	rawInput := hi.resolvedToolInput()
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(rawInput, &args); err != nil {
		// Try as a JSON string containing JSON
		var argsStr string
		if err := json.Unmarshal(rawInput, &argsStr); err == nil {
			_ = json.Unmarshal([]byte(argsStr), &args)
		}
	}

	if args.Command == "" {
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		cfg = config.DefaultConfig()
	}

	// POST to proxy hook endpoint with full context
	hookURL := fmt.Sprintf("http://localhost:%d/hooks/pre-tool-use", cfg.ProxyPort)
	reqBody, _ := json.Marshal(map[string]interface{}{
		"session_id":      hi.SessionID,
		"hook_event_name": "PreToolUse",
		"tool_name":       "Bash",
		"tool_input":      map[string]string{"command": args.Command},
		"tool_use_id":     hi.ToolUseID,
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
	defer func() { _ = resp.Body.Close() }()

	// Reset health state on successful contact
	resetHookHealthState()

	respBody, _ := io.ReadAll(resp.Body)

	// Parse proxy response to check for deny
	var hookResp struct {
		HookSpecificOutput *struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(respBody, &hookResp); err != nil ||
		hookResp.HookSpecificOutput == nil ||
		hookResp.HookSpecificOutput.PermissionDecision != "deny" {
		// Allow (no output)
		return nil
	}

	reason := hookResp.HookSpecificOutput.PermissionDecisionReason

	// Output deny in the correct format for the caller
	if hi.isCopilotFormat() {
		// Copilot expects camelCase flat object
		output := map[string]string{
			"permissionDecision":       "deny",
			"permissionDecisionReason": reason,
		}
		return json.NewEncoder(os.Stdout).Encode(output)
	}

	// Claude Code expects {"decision": "block", "reason": "..."}
	output := map[string]string{
		"decision": "block",
		"reason":   reason,
	}
	return json.NewEncoder(os.Stdout).Encode(output)
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
