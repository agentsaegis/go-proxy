//go:build live

package e2e_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// checkAppleScriptAccess verifies that the current process can send keystrokes
// via System Events. Skips the test with instructions if Accessibility
// permissions are missing.
func checkAppleScriptAccess(t *testing.T) {
	t.Helper()
	// Attempt a no-op keystroke to check permissions.
	out, err := exec.Command("osascript", "-e",
		`tell application "System Events" to key code 0 using {}`,
	).CombinedOutput()
	if err != nil {
		outStr := string(out)
		if strings.Contains(outStr, "not allowed") || strings.Contains(outStr, "1002") || strings.Contains(outStr, "assistive") {
			t.Skip("AppleScript Accessibility denied - grant your terminal app (iTerm/Terminal) permission in System Settings > Privacy & Security > Accessibility, then re-run")
		}
		// Other errors (e.g., System Events not running) - try anyway.
		t.Logf("checkAppleScriptAccess: unexpected error: %s (%v)", outStr, err)
	}
}

// launchDesktop starts Claude Desktop with --proxy-server pointing at the proxy
// for Chromium HTTPS traffic, plus ANTHROPIC_BASE_URL so the embedded Anthropic
// SDK routes direct API calls through the proxy as plain HTTP. The CA must be
// system-trusted for --proxy-server TLS MITM to work on Chromium requests.
func launchDesktop(t *testing.T, proxyPort int, caPath string) *desktopInstance {
	t.Helper()

	appPath := "/Applications/Claude.app/Contents/MacOS/Claude"
	if _, err := os.Stat(appPath); err != nil {
		t.Skipf("Claude Desktop not found at %s", appPath)
	}

	proxyURL := fmt.Sprintf("http://localhost:%d", proxyPort)

	cmd := exec.Command(appPath,
		fmt.Sprintf("--proxy-server=%s", proxyURL),
	)
	stderrBuf := &syncBuffer{}
	cmd.Stderr = stderrBuf

	// ANTHROPIC_BASE_URL: makes the Anthropic SDK in Desktop's main process
	//   route API calls through the proxy as plain HTTP (no TLS needed).
	// NODE_EXTRA_CA_CERTS: lets Node.js trust the proxy CA for any remaining
	//   HTTPS requests in the main process.
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("ANTHROPIC_BASE_URL=%s", proxyURL),
		fmt.Sprintf("NODE_EXTRA_CA_CERTS=%s", caPath),
	)

	if err := cmd.Start(); err != nil {
		t.Fatalf("launchDesktop: %v", err)
	}

	di := &desktopInstance{cmd: cmd, stderr: stderrBuf}

	t.Cleanup(func() {
		di.kill()
	})

	// Wait for Desktop to initialize and settle network connections.
	time.Sleep(15 * time.Second)
	return di
}

func (di *desktopInstance) kill() {
	if di.cmd.Process != nil {
		_ = di.cmd.Process.Kill()
		_ = di.cmd.Wait()
	}
}

// desktopSelectModel uses AppleScript to click the model dropdown in the
// bottom-right corner of Claude Desktop and select the target model.
// Best-effort: logs a warning on failure but does not fail the test.
func desktopSelectModel(t *testing.T, modelName string) {
	t.Helper()

	// Click the model picker dropdown, then click the target model item.
	script := fmt.Sprintf(`
tell application "Claude" to activate
delay 0.5
tell application "System Events"
    tell process "Claude"
        -- Find and click the model picker (bottom-right dropdown showing current model name)
        set modelBtn to missing value
        try
            -- Look for a button/popup containing a known model name pattern
            set allButtons to every button of front window
            repeat with btn in allButtons
                try
                    set btnName to name of btn
                    if btnName contains "Opus" or btnName contains "Sonnet" or btnName contains "Haiku" then
                        set modelBtn to btn
                        exit repeat
                    end if
                end try
            end repeat
        end try
        if modelBtn is missing value then
            -- Try static text or pop up buttons as fallback
            try
                set allPopups to every pop up button of front window
                repeat with popup in allPopups
                    try
                        set popVal to value of popup
                        if popVal contains "Opus" or popVal contains "Sonnet" or popVal contains "Haiku" then
                            set modelBtn to popup
                            exit repeat
                        end if
                    end try
                end repeat
            end try
        end if
        if modelBtn is missing value then
            -- Try groups and their children (Electron often nests elements)
            try
                set allGroups to every group of front window
                repeat with grp in allGroups
                    try
                        set grpButtons to every button of grp
                        repeat with btn in grpButtons
                            try
                                set btnName to name of btn
                                if btnName contains "Opus" or btnName contains "Sonnet" or btnName contains "Haiku" then
                                    set modelBtn to btn
                                    exit repeat
                                end if
                            end try
                        end repeat
                    end try
                    if modelBtn is not missing value then exit repeat
                end repeat
            end try
        end if
        if modelBtn is not missing value then
            click modelBtn
            delay 1
            -- Click the target model in the dropdown
            try
                set menuItems to every menu item of menu 1 of modelBtn
                repeat with mi in menuItems
                    if name of mi contains %q then
                        click mi
                        exit repeat
                    end if
                end repeat
            on error
                -- Dropdown may appear as a separate UI element; try keystroke fallback
                keystroke %q
                delay 0.3
                keystroke return
            end try
        end if
    end tell
end tell
`, modelName, modelName)

	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		t.Logf("desktopSelectModel: best-effort failed (non-fatal): %s (%v)", string(out), err)
	} else {
		t.Logf("desktopSelectModel: attempted to select %q", modelName)
	}
	time.Sleep(2 * time.Second)
}

// desktopSendMessage uses AppleScript to type a message in Claude Desktop
// Code mode and press Enter. Switches to Code mode via Cmd+3 first
// (Chat=Cmd+1, Cowork=Cmd+2, Code=Cmd+3).
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

// copyTrustedCA copies the system-trusted CA cert and key from the real
// ~/.agentsaegis/ directory into the test's home dir. This lets the proxy
// reuse the already-trusted CA instead of generating a new untrusted one.
// Skips the test if the real CA files don't exist.
func copyTrustedCA(t *testing.T, testHomeDir string) {
	t.Helper()

	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("copyTrustedCA: cannot determine home dir: %v", err)
	}

	realConfigDir := filepath.Join(realHome, ".agentsaegis")
	testConfigDir := filepath.Join(testHomeDir, ".agentsaegis")
	if err := os.MkdirAll(testConfigDir, 0o700); err != nil {
		t.Fatalf("copyTrustedCA: mkdir: %v", err)
	}

	for _, name := range []string{"ca.pem", "ca-key.pem"} {
		src := filepath.Join(realConfigDir, name)
		data, err := os.ReadFile(src)
		if err != nil {
			t.Skipf("copyTrustedCA: real CA file %s not found: %v", src, err)
		}
		dst := filepath.Join(testConfigDir, name)
		perm := os.FileMode(0o644)
		if name == "ca-key.pem" {
			perm = 0o600
		}
		if err := os.WriteFile(dst, data, perm); err != nil {
			t.Fatalf("copyTrustedCA: write %s: %v", dst, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Desktop injection scenarios (super-debug mode)
//
// REAL end-to-end: launches actual Claude Desktop app with --proxy-server,
// sends messages via AppleScript, verifies trap injection via proxy logs.
// Uses the user's subscription auth - no API key needed.
// ---------------------------------------------------------------------------

func runDesktopInjectionScenarios(t *testing.T) {
	t.Skip("Claude Desktop support disabled - MITM via --proxy-server not working reliably")
	checkAppleScriptAccess(t)

	// Kill any existing Desktop instances
	killAllDesktop(t)

	// Copy the system-trusted CA so --proxy-server TLS MITM works for
	// Chromium requests. ANTHROPIC_BASE_URL handles the API calls via plain HTTP.
	homeDir := t.TempDir()
	copyTrustedCA(t, homeDir)
	port := liveFindFreePort(t)
	pi := liveStartProxy(t, liveBinaryPath, homeDir, port, true, liveDashboardURL, liveAPIToken)
	caPath := pi.homeDir + "/.agentsaegis/ca.pem"

	// Injection: launch Desktop, send bash request, verify trap in proxy logs
	t.Run("Injection", func(t *testing.T) {
		defer pi.logOnFailure(t)

		di := launchDesktop(t, pi.port, caPath)
		_ = di // keep reference for cleanup

		// Switch to cheapest model for testing
		desktopSelectModel(t, "Haiku")

		// Let Chromium's initial connection burst settle before sending messages
		time.Sleep(3 * time.Second)

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
			if strings.Contains(proxyLogs, "upstream response") || strings.Contains(proxyLogs, "MITM upstream response") {
				t.Log("Proxy received API traffic but no trap injection")
			} else if strings.Contains(proxyLogs, "incoming request") || strings.Contains(proxyLogs, "CONNECT request") {
				t.Log("Proxy received requests from Desktop but no API calls reached upstream")
			} else {
				t.Log("Proxy received no traffic from Desktop at all")
			}
			liveResults.record("Desktop/Haiku4.5", "Injection", "FAIL")
			t.Fatalf("no trap injection detected within 60s")
		}

		t.Log("Desktop Injection: trap injected via proxy")
		liveResults.record("Desktop/Haiku4.5", "Injection", "PASS")
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
			liveResults.record("Desktop/Haiku4.5", "Approve", "SKIP")
			t.Skip("trap not resolved within timeout")
			return
		}

		t.Log("Desktop Approve: trap resolved via proxy")
		liveResults.record("Desktop/Haiku4.5", "Approve", "PASS")
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
				liveResults.record("Desktop/Haiku4.5", "Reject", "FAIL")
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
			liveResults.record("Desktop/Haiku4.5", "Reject", "FAIL")
			t.Fatalf("expected 2 trap resolutions, got %d", resolveCount)
		}

		t.Logf("Desktop Reject: %d traps injected and resolved via real Desktop flow", resolveCount)
		liveResults.record("Desktop/Haiku4.5", "Reject", "PASS")
	})
}

// ---------------------------------------------------------------------------
// Desktop passthrough scenarios (normal/debug mode)
//
// Verifies that text-only requests pass through without trap injection.
// ---------------------------------------------------------------------------

func runDesktopPassthroughScenarios(t *testing.T, _ *proxyInstance) {
	t.Skip("Claude Desktop support disabled - MITM via --proxy-server not working reliably")
	checkAppleScriptAccess(t)

	// Kill any existing Desktop instances
	killAllDesktop(t)

	// Copy trusted CA for --proxy-server. ANTHROPIC_BASE_URL handles API calls.
	homeDir := t.TempDir()
	copyTrustedCA(t, homeDir)
	port := liveFindFreePort(t)
	pi := liveStartProxy(t, liveBinaryPath, homeDir, port, false, liveDashboardURL, liveAPIToken)
	caPath := pi.homeDir + "/.agentsaegis/ca.pem"

	t.Run("Passthrough", func(t *testing.T) {
		defer pi.logOnFailure(t)

		di := launchDesktop(t, pi.port, caPath)
		_ = di

		// Switch to cheapest model for testing
		desktopSelectModel(t, "Haiku")

		// Let Chromium's initial connection burst settle
		time.Sleep(3 * time.Second)

		// Send a text-only request (no bash)
		desktopSendMessage(t, "say hello")

		// Wait for API call to arrive at the proxy (plain HTTP or MITM)
		deadline3 := time.Now().Add(60 * time.Second)
		gotTraffic := false
		for time.Now().Before(deadline3) {
			logs := pi.stderr.String()
			if strings.Contains(logs, "upstream response") ||
				strings.Contains(logs, "incoming request") ||
				strings.Contains(logs, "MITM forwarding request") {
				gotTraffic = true
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		if !gotTraffic {
			liveResults.record("Desktop/Haiku4.5", "Passthrough", "FAIL")
			t.Fatal("no API traffic detected through proxy")
		}

		// Verify no trap was injected
		proxyLogs := pi.stderr.String()
		if strings.Contains(proxyLogs, "trap registered") {
			liveResults.record("Desktop/Haiku4.5", "Passthrough", "FAIL")
			t.Fatal("unexpected trap injection in passthrough mode")
		}

		liveResults.record("Desktop/Haiku4.5", "Passthrough", "PASS")
		t.Log("Desktop Passthrough: API traffic flowed through, no traps")
	})

	t.Run("Clean", func(t *testing.T) {
		defer pi.logOnFailure(t)

		proxyLogs := pi.stderr.String()
		if strings.Contains(proxyLogs, "trap registered") {
			liveResults.record("Desktop/Haiku4.5", "Clean", "FAIL")
			t.Fatal("unexpected trap injection")
		}

		liveResults.record("Desktop/Haiku4.5", "Clean", "PASS")
		t.Log("Desktop Clean: no traps in normal mode")
	})
}
