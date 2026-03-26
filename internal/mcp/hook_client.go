package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// HookChecker checks a command against the proxy's hook endpoint.
type HookChecker interface {
	CheckCommand(ctx context.Context, sessionID, command, toolUseID string) (*HookResult, error)
}

// HookResult describes the proxy's decision for a command.
type HookResult struct {
	Allowed bool
	Reason  string // deny reason, empty if allowed
}

// HookClient communicates with the AgentsAegis proxy's hook endpoint.
type HookClient struct {
	baseURL    string
	hookSecret string
	httpClient *http.Client
	logger     *slog.Logger
}

// hookRequest matches the HookRequest struct in internal/server/hook.go.
type hookRequest struct {
	SessionID     string          `json:"session_id"`
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	ToolUseID     string          `json:"tool_use_id"`
}

// hookResponse matches the HookResponse struct in internal/server/hook.go.
type hookResponse struct {
	HookSpecificOutput *hookOutput `json:"hookSpecificOutput,omitempty"`
}

type hookOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

// NewHookClient creates a HookClient targeting the proxy at the given base URL.
func NewHookClient(baseURL, hookSecret string, logger *slog.Logger) *HookClient {
	return &HookClient{
		baseURL:    baseURL,
		hookSecret: hookSecret,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		logger:     logger,
	}
}

// CheckCommand POSTs to the proxy's PreToolUse hook endpoint.
// On any error (timeout, connection refused), returns allowed=true (fail-open).
func (c *HookClient) CheckCommand(ctx context.Context, sessionID, command, toolUseID string) (*HookResult, error) {
	toolInput, _ := json.Marshal(map[string]string{"command": command})

	req := hookRequest{
		SessionID:     sessionID,
		HookEventName: "PreToolUse",
		ToolName:      "Bash",
		ToolInput:     toolInput,
		ToolUseID:     toolUseID,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return &HookResult{Allowed: true}, fmt.Errorf("marshaling hook request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/hooks/pre-tool-use", bytes.NewReader(body))
	if err != nil {
		return &HookResult{Allowed: true}, fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.hookSecret != "" {
		httpReq.Header.Set("X-Hook-Secret", c.hookSecret)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		c.logger.Debug("hook endpoint unreachable, fail-open", "error", err)
		return &HookResult{Allowed: true}, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Debug("failed to read hook response, fail-open", "error", err)
		return &HookResult{Allowed: true}, err
	}

	if resp.StatusCode != http.StatusOK {
		c.logger.Debug("hook returned non-200, fail-open", "status", resp.StatusCode)
		return &HookResult{Allowed: true}, fmt.Errorf("hook returned status %d", resp.StatusCode)
	}

	var hookResp hookResponse
	if err := json.Unmarshal(respBody, &hookResp); err != nil {
		c.logger.Debug("failed to parse hook response, fail-open", "error", err)
		return &HookResult{Allowed: true}, nil
	}

	if hookResp.HookSpecificOutput != nil && hookResp.HookSpecificOutput.PermissionDecision == "deny" {
		return &HookResult{
			Allowed: false,
			Reason:  hookResp.HookSpecificOutput.PermissionDecisionReason,
		}, nil
	}

	return &HookResult{Allowed: true}, nil
}

// InjectResult describes whether the proxy decided to inject a trap.
type InjectResult struct {
	Inject      bool   `json:"inject"`
	TrapCommand string `json:"trap_command,omitempty"`
}

// CheckInjectTrap POSTs to the proxy's inject-trap endpoint to check if a trap should be injected.
// On any error, returns no injection (fail-open).
func (c *HookClient) CheckInjectTrap(ctx context.Context, sessionID, command string) (*InjectResult, error) {
	toolInput, _ := json.Marshal(map[string]string{"command": command})

	req := hookRequest{
		SessionID:     sessionID,
		HookEventName: "PreToolUse",
		ToolName:      "Bash",
		ToolInput:     toolInput,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return &InjectResult{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/hooks/inject-trap", bytes.NewReader(body))
	if err != nil {
		return &InjectResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return &InjectResult{}, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return &InjectResult{}, err
	}

	var result InjectResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return &InjectResult{}, err
	}

	return &result, nil
}
