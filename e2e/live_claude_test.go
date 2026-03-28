//go:build live

package e2e_test

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Claude injection scenarios (super-debug mode)
//
// Uses `claude -p` (print mode) with the user's subscription auth.
// The proxy in super-debug mode injects a trap on every bash tool_use.
// Claude Code auto-executes the harmless canary command, sends the
// tool_result back, and the proxy detects it via the request-body path.
// Verification is via proxy stderr logs.
// ---------------------------------------------------------------------------

func runClaudeInjectionScenarios(t *testing.T, pi *proxyInstance) {
	if _, err := exec.LookPath("claude"); err != nil {
		liveResults.record("Claude", "Injection", "SKIP")
		liveResults.record("Claude", "Approve", "SKIP")
		liveResults.record("Claude", "Reject", "SKIP")
		t.Skip("claude CLI not found in PATH")
		return
	}

	// Single test covers both Injection and Approve:
	// - Injection: proxy injects trap into SSE response
	// - Approve: Claude auto-executes trap, proxy detects via request body = "missed"
	t.Run("Approve", func(t *testing.T) {
		defer pi.logOnFailure(t)

		t.Log("Running claude -p through super-debug proxy...")
		result := runClaudeCLI(t, pi.port, liveBinaryPath,
			"Use the Bash tool to run this exact command: ls /tmp",
			90*time.Second)

		t.Logf("Claude exit=%d, stdout=%d bytes, stderr=%d bytes",
			result.ExitCode, len(result.Stdout), len(result.Stderr))
		if result.Stderr != "" {
			t.Logf("Claude stderr (first 500): %s", truncate(result.Stderr, 500))
		}

		// Check proxy logs for trap injection
		proxyLogs := pi.stderr.String()

		if !strings.Contains(proxyLogs, "trap registered") {
			liveResults.record("Claude", "Injection", "FAIL")
			liveResults.record("Claude", "Approve", "FAIL")
			t.Logf("Proxy logs (last 2000): %s", truncate(proxyLogs, 2000))
			t.Fatal("proxy did not inject a trap (no 'trap registered' in logs)")
		}
		liveResults.record("Claude", "Injection", "PASS")
		t.Log("Claude Injection: trap registered in proxy logs")

		// Check proxy logs for trap resolution
		if !strings.Contains(proxyLogs, "trap resolved") {
			liveResults.record("Claude", "Approve", "FAIL")
			t.Logf("Proxy logs (last 2000): %s", truncate(proxyLogs, 2000))
			t.Fatal("proxy did not resolve the trap (no 'trap resolved' in logs)")
		}

		// Check the resolution was "missed" (developer approved the trap)
		if !strings.Contains(proxyLogs, `result=missed`) {
			t.Log("Warning: trap resolved but result may not be 'missed'")
		}

		// Log which path resolved the trap (hook vs request-body)
		if strings.Contains(proxyLogs, "hook command match") {
			t.Log("Claude Approve: resolved via PreToolUse HOOK path")
		} else if strings.Contains(proxyLogs, "trap result detected from request body") {
			t.Log("Claude Approve: resolved via request-body fallback path")
		} else {
			t.Log("Claude Approve: resolution path unclear from logs")
		}

		liveResults.record("Claude", "Approve", "PASS")
	})

	// Reject (caught) scenario: custom hook detects the trap and sends a
	// MODIFIED command to the proxy (same tool_use_id, different command).
	// Proxy resolves as "caught" (user noticed and edited the command).
	t.Run("Reject", func(t *testing.T) {
		defer pi.logOnFailure(t)

		// Need a fresh proxy for clean logs (previous test's logs are still in buffer)
		homeDir2 := t.TempDir()
		port2 := liveFindFreePort(t)
		pi2 := liveStartProxy(t, liveBinaryPath, homeDir2, port2, true, liveDashboardURL, liveAPIToken)

		t.Log("Running claude -p with catch-mode hook...")
		result := runClaudeCLICatchMode(t, pi2.port,
			"Use the Bash tool to run this exact command: ls /tmp",
			90*time.Second)

		t.Logf("Claude Reject: exit=%d, stdout=%d bytes", result.ExitCode, len(result.Stdout))

		proxyLogs := pi2.stderr.String()

		if !strings.Contains(proxyLogs, "trap registered") {
			liveResults.record("Claude", "Reject", "FAIL")
			t.Logf("Proxy logs: %s", truncate(proxyLogs, 2000))
			t.Fatal("proxy did not inject a trap")
		}

		// The catch-mode hook sends modified command with same tool_use_id.
		// Proxy should resolve as "caught".
		if strings.Contains(proxyLogs, "trap caught: user edited command") {
			t.Log("Claude Reject: trap resolved as CAUGHT via hook (user edited command)")
			liveResults.record("Claude", "Reject", "PASS")
		} else if strings.Contains(proxyLogs, `result=caught`) {
			t.Log("Claude Reject: trap resolved as CAUGHT")
			liveResults.record("Claude", "Reject", "PASS")
		} else if strings.Contains(proxyLogs, "trap resolved") {
			// Trap resolved but not as caught - check what happened
			t.Logf("Proxy logs: %s", truncate(proxyLogs, 2000))
			liveResults.record("Claude", "Reject", "FAIL")
			t.Fatal("trap resolved but not as 'caught'")
		} else {
			t.Logf("Proxy logs: %s", truncate(proxyLogs, 2000))
			liveResults.record("Claude", "Reject", "FAIL")
			t.Fatal("trap was not resolved")
		}
	})
}

// ---------------------------------------------------------------------------
// Claude passthrough scenarios (normal/debug mode)
//
// Proxy runs without super-debug, so no traps should be injected for
// a simple text prompt.
// ---------------------------------------------------------------------------

func runClaudePassthroughScenarios(t *testing.T, pi *proxyInstance) {
	if _, err := exec.LookPath("claude"); err != nil {
		liveResults.record("Claude", "Passthrough", "SKIP")
		liveResults.record("Claude", "Clean", "SKIP")
		t.Skip("claude CLI not found in PATH")
		return
	}

	t.Run("Passthrough", func(t *testing.T) {
		defer pi.logOnFailure(t)

		result := runClaudeCLI(t, pi.port, liveBinaryPath,
			"Reply with just the word 'hello'. Do not use any tools.",
			60*time.Second)

		t.Logf("Claude Passthrough: exit=%d, stdout=%d bytes",
			result.ExitCode, len(result.Stdout))

		// Should get a response (exit 0 or at least some output)
		if result.Stdout == "" && result.ExitCode != 0 {
			liveResults.record("Claude", "Passthrough", "FAIL")
			t.Logf("Claude stderr: %s", truncate(result.Stderr, 500))
			t.Fatal("claude -p returned no output and non-zero exit")
		}

		// No trap should be injected in normal mode for a text-only prompt
		proxyLogs := pi.stderr.String()
		if strings.Contains(proxyLogs, "trap registered") {
			liveResults.record("Claude", "Passthrough", "FAIL")
			t.Fatal("unexpected trap injection in passthrough mode")
		}

		liveResults.record("Claude", "Passthrough", "PASS")
		t.Log("Claude Passthrough: response received, no trap injected")
	})

	t.Run("Clean", func(t *testing.T) {
		defer pi.logOnFailure(t)

		result := runClaudeCLI(t, pi.port, liveBinaryPath,
			"Reply with just the word 'hello'. Do not use any tools.",
			60*time.Second)

		t.Logf("Claude Clean: exit=%d, stdout=%d bytes",
			result.ExitCode, len(result.Stdout))

		if result.Stdout == "" && result.ExitCode != 0 {
			liveResults.record("Claude", "Clean", "FAIL")
			t.Logf("Claude stderr: %s", truncate(result.Stderr, 500))
			t.Fatal("claude -p returned no output and non-zero exit")
		}

		proxyLogs := pi.stderr.String()
		if strings.Contains(proxyLogs, "trap registered") {
			liveResults.record("Claude", "Clean", "FAIL")
			t.Fatal("unexpected trap injection in clean mode")
		}

		liveResults.record("Claude", "Clean", "PASS")
		t.Log("Claude Clean: response received, no trap injected")
	})
}
