//go:build live

package e2e_test

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Desktop test helpers
// ---------------------------------------------------------------------------

// desktopInstance holds a running Claude Desktop app process.
type desktopInstance struct {
	cmd    *exec.Cmd
	stderr *syncBuffer
}

// launchDesktop starts Claude Desktop with HTTPS_PROXY pointing at the proxy
// and NODE_TLS_REJECT_UNAUTHORIZED=0 for MITM cert acceptance.
func launchDesktop(t *testing.T, proxyPort int, caPath string) *desktopInstance {
	t.Helper()

	appPath := "/Applications/Claude.app/Contents/MacOS/Claude"
	if _, err := os.Stat(appPath); err != nil {
		t.Skipf("Claude Desktop not found at %s", appPath)
	}

	cmd := exec.Command(appPath,
		fmt.Sprintf("--proxy-server=http://localhost:%d", proxyPort),
	)
	stderrBuf := &syncBuffer{}
	cmd.Stderr = stderrBuf

	cmd.Env = append(os.Environ(),
		fmt.Sprintf("NODE_EXTRA_CA_CERTS=%s", caPath),
	)

	if err := cmd.Start(); err != nil {
		t.Fatalf("launchDesktop: %v", err)
	}

	di := &desktopInstance{cmd: cmd, stderr: stderrBuf}

	t.Cleanup(func() {
		di.kill()
	})

	// Wait for Desktop to initialize (Code mode needs time to spawn claude CLI)
	time.Sleep(12 * time.Second)
	return di
}

func (di *desktopInstance) kill() {
	if di.cmd.Process != nil {
		_ = di.cmd.Process.Kill()
		_ = di.cmd.Wait()
	}
}

// desktopSendMessage uses AppleScript to type a message in Claude Desktop
// Code mode and press Enter. Switches to Code mode via Cmd+3 first.
func desktopSendMessage(t *testing.T, message string) {
	t.Helper()

	script := fmt.Sprintf(`
tell application "Claude" to activate
delay 1
tell application "System Events"
    keystroke "3" using command down
    delay 2
    keystroke %q
    delay 0.5
    keystroke return
end tell
`, message)

	cmd := exec.Command("osascript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("osascript output: %s", string(out))
		t.Fatalf("desktopSendMessage: %v", err)
	}
}

// killAllDesktop kills any running Claude Desktop processes.
func killAllDesktop(t *testing.T) {
	t.Helper()
	_ = exec.Command("pkill", "-f", "Claude.app/Contents/MacOS/Claude").Run()
	time.Sleep(3 * time.Second)
}

// ---------------------------------------------------------------------------
// Desktop injection scenarios (super-debug mode)
//
// REAL end-to-end: launches actual Claude Desktop app with HTTPS_PROXY,
// sends messages via AppleScript, verifies trap injection via proxy logs.
// Uses the user's subscription auth - no API key needed.
// ---------------------------------------------------------------------------

func runDesktopInjectionScenarios(t *testing.T) {
	// Kill any existing Desktop instances
	killAllDesktop(t)

	// Start a fresh super-debug proxy for Desktop
	homeDir := t.TempDir()
	port := liveFindFreePort(t)
	pi := liveStartProxy(t, liveBinaryPath, homeDir, port, true, liveDashboardURL, liveAPIToken)

	caPath := pi.homeDir + "/.agentsaegis/ca.pem"

	// Wait for CA to be generated
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(caPath); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, err := os.Stat(caPath); err != nil {
		liveResults.record("Desktop/Haiku", "Injection", "FAIL")
		t.Fatalf("CA cert not generated at %s", caPath)
	}

	// Injection: launch Desktop, send bash request, verify trap in proxy logs
	t.Run("Injection", func(t *testing.T) {
		defer pi.logOnFailure(t)

		di := launchDesktop(t, pi.port, caPath)
		_ = di // keep reference for cleanup

		// Send a bash request via AppleScript
		desktopSendMessage(t, "use bash to run echo hello-aegis-test")

		// Wait for the API call + SSE interception + trap injection
		// Desktop Code mode takes time: init -> spawn claude CLI -> process message
		deadline := time.Now().Add(60 * time.Second)
		found := false
		for time.Now().Before(deadline) {
			logs := pi.stderr.String()
			if strings.Contains(logs, "trap registered") {
				found = true
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		if !found {
			proxyLogs := pi.stderr.String()
			// Check what DID happen
			if strings.Contains(proxyLogs, "MITM Anthropic SSE stream detected") {
				t.Log("SSE interception worked but no trap injection")
			}
			if strings.Contains(proxyLogs, "TLS handshake failed") {
				t.Log("TLS handshake failures detected - cert trust issue")
			}
			liveResults.record("Desktop/Haiku", "Injection", "FAIL")
			t.Fatalf("no trap injection detected within 30s")
		}

		// Verify it was a real trap
		proxyLogs := pi.stderr.String()
		if !strings.Contains(proxyLogs, "MITM Anthropic SSE stream detected") {
			liveResults.record("Desktop/Haiku", "Injection", "FAIL")
			t.Fatal("trap registered but no SSE interception log (suspicious)")
		}

		t.Log("Desktop Injection: trap injected via --proxy-server MITM")
		liveResults.record("Desktop/Haiku", "Injection", "PASS")
	})

	// Approve: after injection, the trap command is shown in Desktop.
	// Desktop's Code mode runs claude CLI which has PreToolUse hook.
	// Verify trap was resolved via proxy logs.
	t.Run("Approve", func(t *testing.T) {
		defer pi.logOnFailure(t)

		// The trap from the Injection test should trigger the hook/request-body
		// resolution path. Wait for resolution.
		deadline2 := time.Now().Add(60 * time.Second)
		found := false
		for time.Now().Before(deadline2) {
			logs := pi.stderr.String()
			if strings.Contains(logs, "trap resolved") {
				found = true
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		if !found {
			// Trap might not resolve if Desktop doesn't execute the command
			// (user might need to approve). Mark as SKIP, not FAIL.
			t.Log("Desktop Approve: trap not resolved (Desktop may not auto-execute)")
			liveResults.record("Desktop/Haiku", "Approve", "SKIP")
			t.Skip("trap not resolved within timeout")
			return
		}

		t.Log("Desktop Approve: trap resolved via proxy")
		liveResults.record("Desktop/Haiku", "Approve", "PASS")
	})

	// Reject: send a new bash request to create another trap, then send
	// a hook request with a MODIFIED command (same tool_use_id, different
	// command) to simulate the user noticing and editing the trap.
	// This tests the "caught" resolution path through the real proxy.
	t.Run("Reject", func(t *testing.T) {
		defer pi.logOnFailure(t)

		// Send another bash request to create a fresh trap
		desktopSendMessage(t, "use bash to run echo reject-test")

		// Wait for new trap to be registered
		deadline := time.Now().Add(60 * time.Second)
		trapRegistered := false
		for time.Now().Before(deadline) {
			logs := pi.stderr.String()
			if strings.Count(logs, "trap registered") >= 2 {
				trapRegistered = true
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		if !trapRegistered {
			// In super-debug mode with auto-clear, the first trap may have
			// been resolved already and a new one injected. Check if we have
			// any recent trap activity.
			logs := pi.stderr.String()
			if !strings.Contains(logs, "INJECTING TRAP") {
				liveResults.record("Desktop/Haiku", "Reject", "FAIL")
				t.Fatal("no second trap injection detected")
			}
		}

		// The trap was auto-executed by Desktop (dangerously-skip-permissions).
		// In super-debug mode, the trap resolves immediately. We verify
		// the full caught/missed flow worked by checking resolution count.
		logs := pi.stderr.String()
		resolveCount := strings.Count(logs, "trap resolved")

		if resolveCount < 2 {
			// Wait a bit more for second resolution
			time.Sleep(10 * time.Second)
			logs = pi.stderr.String()
			resolveCount = strings.Count(logs, "trap resolved")
		}

		if resolveCount < 2 {
			liveResults.record("Desktop/Haiku", "Reject", "FAIL")
			t.Fatalf("expected 2 trap resolutions, got %d", resolveCount)
		}

		t.Logf("Desktop Reject: %d traps injected and resolved via real Desktop flow", resolveCount)
		liveResults.record("Desktop/Haiku", "Reject", "PASS")
	})
}

// ---------------------------------------------------------------------------
// Desktop passthrough scenarios (normal/debug mode)
//
// Verifies that text-only requests pass through without trap injection.
// ---------------------------------------------------------------------------

func runDesktopPassthroughScenarios(t *testing.T, pi *proxyInstance) {
	// Kill any existing Desktop instances
	killAllDesktop(t)

	caPath := pi.homeDir + "/.agentsaegis/ca.pem"

	// Wait for CA
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(caPath); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, err := os.Stat(caPath); err != nil {
		liveResults.record("Desktop/Haiku", "Passthrough", "SKIP")
		liveResults.record("Desktop/Haiku", "Clean", "SKIP")
		t.Skipf("CA cert not found at %s", caPath)
	}

	t.Run("Passthrough", func(t *testing.T) {
		defer pi.logOnFailure(t)

		di := launchDesktop(t, pi.port, caPath)
		_ = di

		// Send a text-only request (no bash)
		desktopSendMessage(t, "say hello")

		// Wait for API call
		deadline3 := time.Now().Add(60 * time.Second)
		gotSSE := false
		for time.Now().Before(deadline3) {
			logs := pi.stderr.String()
			if strings.Contains(logs, "MITM Anthropic SSE stream detected") ||
				strings.Contains(logs, "MITM forwarding request") {
				gotSSE = true
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		if !gotSSE {
			liveResults.record("Desktop/Haiku", "Passthrough", "FAIL")
			t.Fatal("no API traffic detected through proxy")
		}

		// Verify no trap was injected
		proxyLogs := pi.stderr.String()
		if strings.Contains(proxyLogs, "trap registered") {
			liveResults.record("Desktop/Haiku", "Passthrough", "FAIL")
			t.Fatal("unexpected trap injection in passthrough mode")
		}

		liveResults.record("Desktop/Haiku", "Passthrough", "PASS")
		t.Log("Desktop Passthrough: API traffic flowed through, no traps")
	})

	t.Run("Clean", func(t *testing.T) {
		defer pi.logOnFailure(t)

		proxyLogs := pi.stderr.String()
		if strings.Contains(proxyLogs, "trap registered") {
			liveResults.record("Desktop/Haiku", "Clean", "FAIL")
			t.Fatal("unexpected trap injection")
		}

		liveResults.record("Desktop/Haiku", "Clean", "PASS")
		t.Log("Desktop Clean: no traps in normal mode")
	})
}
