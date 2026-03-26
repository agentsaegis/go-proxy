//go:build live

package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Claude helpers
// ---------------------------------------------------------------------------

// claudeRequestBody builds an Anthropic Messages API request body.
func claudeRequestBody(prompt string, stream bool) []byte {
	body := map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"max_tokens": 256,
		"stream":     stream,
		"messages": []map[string]interface{}{
			{"role": "user", "content": prompt},
		},
	}
	data, _ := json.Marshal(body)
	return data
}

// sendClaudeSSE sends a streaming Claude API request through the proxy and
// returns the entire response body as bytes.
func sendClaudeSSE(t *testing.T, proxyURL, apiKey, prompt string) []byte {
	t.Helper()

	body := claudeRequestBody(prompt, true)
	req, err := http.NewRequest(http.MethodPost, proxyURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("sendClaudeSSE: build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("sendClaudeSSE: request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("sendClaudeSSE: status %d: %s", resp.StatusCode, string(respBody))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("sendClaudeSSE: read body: %v", err)
	}
	return data
}

// sendClaudeHook sends a PreToolUse hook request to the proxy and returns
// the parsed JSON response.
func sendClaudeHook(t *testing.T, proxyURL, sessionID, command, toolUseID string) map[string]interface{} {
	t.Helper()

	hookBody, _ := json.Marshal(map[string]interface{}{
		"session_id":      sessionID,
		"hook_event_name": "PreToolUse",
		"tool_name":       "Bash",
		"tool_input":      map[string]string{"command": command},
		"tool_use_id":     toolUseID,
	})

	resp, err := http.Post(proxyURL+"/hooks/pre-tool-use", "application/json",
		bytes.NewReader(hookBody))
	if err != nil {
		t.Fatalf("sendClaudeHook: request failed: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("sendClaudeHook: decode response: %v", err)
	}
	return result
}

// ---------------------------------------------------------------------------
// Claude injection scenarios (super-debug mode)
// ---------------------------------------------------------------------------

func runClaudeInjectionScenarios(t *testing.T, pi *proxyInstance) {
	if liveAnthropicKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
		liveResults.record("Claude", "Injection", "SKIP")
		liveResults.record("Claude", "Approve", "SKIP")
		liveResults.record("Claude", "Reject", "SKIP")
		return
	}

	t.Run("Injection", func(t *testing.T) {
		defer pi.logOnFailure(t)
		sid := uniqueSessionID("claude", "injection")
		_ = sid // session ID for dashboard isolation - not needed for injection-only test

		body := sendClaudeSSE(t, pi.proxyURL, liveAnthropicKey,
			"Write a single bash command to list files in /tmp. Use the bash tool.")

		text, toolUses := parseAnthropicSSE(t, body)
		t.Logf("Claude Injection: text=%q, toolUses=%d", truncate(text, 80), len(toolUses))

		if len(toolUses) == 0 {
			liveResults.record("Claude", "Injection", "FAIL")
			t.Fatal("expected at least one tool_use block in response")
		}

		// In super-debug mode, the proxy replaces the command with a canary trap
		// (touch /tmp/.aegis_canary_<PID>). Verify the command is a trap, not the original.
		foundTrap := false
		for _, tu := range toolUses {
			if tu.Name == "bash" || strings.Contains(strings.ToLower(tu.Name), "bash") {
				if tu.Command != "" {
					t.Logf("Claude Injection: bash tool_use command=%q", truncate(tu.Command, 120))
					if strings.Contains(tu.Command, "aegis_canary") || strings.Contains(tu.Command, ".aegis-trap") {
						foundTrap = true
					}
				}
			}
		}

		if !foundTrap {
			liveResults.record("Claude", "Injection", "FAIL")
			t.Fatal("no trap command (aegis_canary marker) found in bash tool_use blocks")
		}

		liveResults.record("Claude", "Injection", "PASS")
	})

	t.Run("Approve", func(t *testing.T) {
		defer pi.logOnFailure(t)
		sid := uniqueSessionID("claude", "approve")

		body := sendClaudeSSE(t, pi.proxyURL, liveAnthropicKey,
			"Write a single bash command to list files in /tmp. Use the bash tool.")

		_, toolUses := parseAnthropicSSE(t, body)
		if len(toolUses) == 0 {
			liveResults.record("Claude", "Approve", "FAIL")
			t.Fatal("expected tool_use blocks for approve scenario")
		}

		// Find the bash tool_use with a command
		var trapCmd, toolUseID string
		for _, tu := range toolUses {
			if (tu.Name == "bash" || strings.Contains(strings.ToLower(tu.Name), "bash")) && tu.Command != "" {
				trapCmd = tu.Command
				toolUseID = tu.ID
				break
			}
		}
		if trapCmd == "" {
			liveResults.record("Claude", "Approve", "FAIL")
			t.Fatal("no bash command found in tool_use blocks")
		}

		// Send hook with the MATCHING trap command (simulates developer approving)
		hookResp := sendClaudeHook(t, pi.proxyURL, sid, trapCmd, toolUseID)

		// Assert deny
		output, ok := hookResp["hookSpecificOutput"].(map[string]interface{})
		if !ok {
			liveResults.record("Claude", "Approve", "FAIL")
			t.Fatalf("expected hookSpecificOutput with deny, got: %v", hookResp)
		}
		if output["permissionDecision"] != "deny" {
			liveResults.record("Claude", "Approve", "FAIL")
			t.Fatalf("expected deny, got: %v", output["permissionDecision"])
		}
		t.Logf("Claude Approve: hook correctly denied trap command")

		// Assert dashboard event arrives
		events := queryDashboardEvents(t, liveDashboardURL, liveAPIToken, sid, 15*time.Second)
		found := false
		for _, ev := range events {
			if ev.Result == "missed" {
				found = true
				break
			}
		}
		if !found {
			liveResults.record("Claude", "Approve", "FAIL")
			t.Fatalf("expected dashboard event with result=missed, got: %+v", events)
		}

		liveResults.record("Claude", "Approve", "PASS")
	})

	t.Run("Reject", func(t *testing.T) {
		defer pi.logOnFailure(t)
		sid := uniqueSessionID("claude", "reject")

		body := sendClaudeSSE(t, pi.proxyURL, liveAnthropicKey,
			"Write a single bash command to list files in /tmp. Use the bash tool.")

		_, toolUses := parseAnthropicSSE(t, body)
		if len(toolUses) == 0 {
			liveResults.record("Claude", "Reject", "FAIL")
			t.Fatal("expected tool_use blocks for reject scenario")
		}

		// Find the bash tool_use
		var toolUseID string
		for _, tu := range toolUses {
			if (tu.Name == "bash" || strings.Contains(strings.ToLower(tu.Name), "bash")) && tu.Command != "" {
				toolUseID = tu.ID
				break
			}
		}
		if toolUseID == "" {
			liveResults.record("Claude", "Reject", "FAIL")
			t.Fatal("no bash command found in tool_use blocks")
		}

		// Send hook with a DIFFERENT command (simulates developer running a different command)
		hookResp := sendClaudeHook(t, pi.proxyURL, sid, "echo this-is-not-a-trap", toolUseID)

		// Assert allow (no hookSpecificOutput)
		if _, hasDeny := hookResp["hookSpecificOutput"]; hasDeny {
			liveResults.record("Claude", "Reject", "FAIL")
			t.Fatalf("expected allow (no hookSpecificOutput), got: %v", hookResp)
		}
		t.Logf("Claude Reject: hook correctly allowed non-matching command")

		// Assert no dashboard events
		assertNoDashboardEvents(t, liveDashboardURL, liveAPIToken, sid)

		liveResults.record("Claude", "Reject", "PASS")
	})
}

// ---------------------------------------------------------------------------
// Claude passthrough scenarios (normal/debug mode)
// ---------------------------------------------------------------------------

func runClaudePassthroughScenarios(t *testing.T, pi *proxyInstance) {
	if liveAnthropicKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
		liveResults.record("Claude", "Passthrough", "SKIP")
		liveResults.record("Claude", "Clean", "SKIP")
		return
	}

	t.Run("Passthrough", func(t *testing.T) {
		defer pi.logOnFailure(t)
		sid := uniqueSessionID("claude", "passthrough")
		_ = sid

		body := sendClaudeSSE(t, pi.proxyURL, liveAnthropicKey,
			"Reply with just the word 'hello'. Do not use any tools.")

		text, toolUses := parseAnthropicSSE(t, body)
		t.Logf("Claude Passthrough: text=%q, toolUses=%d", truncate(text, 80), len(toolUses))

		// Should have text response, no bash tool_use
		if text == "" {
			liveResults.record("Claude", "Passthrough", "FAIL")
			t.Fatal("expected non-empty text response")
		}

		for _, tu := range toolUses {
			if tu.Name == "bash" || strings.Contains(strings.ToLower(tu.Name), "bash") {
				liveResults.record("Claude", "Passthrough", "FAIL")
				t.Fatalf("unexpected bash tool_use in passthrough response: %+v", tu)
			}
		}

		// No dashboard events expected
		assertNoDashboardEvents(t, liveDashboardURL, liveAPIToken,
			uniqueSessionID("claude", "passthrough-check"))

		liveResults.record("Claude", "Passthrough", "PASS")
	})

	t.Run("Clean", func(t *testing.T) {
		defer pi.logOnFailure(t)
		sid := uniqueSessionID("claude", "clean")
		_ = sid

		body := sendClaudeSSE(t, pi.proxyURL, liveAnthropicKey,
			"Reply with just the word 'hello'. Do not use any tools.")

		text, toolUses := parseAnthropicSSE(t, body)
		t.Logf("Claude Clean: text=%q, toolUses=%d", truncate(text, 80), len(toolUses))

		if text == "" {
			liveResults.record("Claude", "Clean", "FAIL")
			t.Fatal("expected non-empty text response")
		}

		for _, tu := range toolUses {
			if tu.Name == "bash" || strings.Contains(strings.ToLower(tu.Name), "bash") {
				liveResults.record("Claude", "Clean", "FAIL")
				t.Fatalf("unexpected bash tool_use in clean response: %+v", tu)
			}
		}

		assertNoDashboardEvents(t, liveDashboardURL, liveAPIToken,
			uniqueSessionID("claude", "clean-check"))

		liveResults.record("Claude", "Clean", "PASS")
	})
}

// ---------------------------------------------------------------------------
// Utility
// ---------------------------------------------------------------------------

// truncate shortens a string to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// formatProxyAddr returns the proxy address for use in hook requests (not
// through the CONNECT tunnel).
func formatProxyAddr(pi *proxyInstance) string {
	return fmt.Sprintf("http://127.0.0.1:%d", pi.port)
}
