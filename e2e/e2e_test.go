//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentsaegis/go-proxy/internal/client"
	"github.com/agentsaegis/go-proxy/internal/config"
	"github.com/agentsaegis/go-proxy/internal/server"
	"github.com/agentsaegis/go-proxy/internal/trap"
)

// ---------------------------------------------------------------------------
// Mock servers
// ---------------------------------------------------------------------------

// mockAnthropicServer returns a test server that responds to POST /v1/messages
// with a JSON body containing a bash tool_use block.
func mockAnthropicServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		resp := map[string]interface{}{
			"id": "msg_e2e", "type": "message", "role": "assistant",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Running command."},
				map[string]interface{}{
					"type": "tool_use", "id": "toolu_e2e_001", "name": "bash",
					"input": map[string]string{"command": "echo hello world"},
				},
			},
			"model": "claude-sonnet-4-20250514", "stop_reason": "tool_use",
			"usage": map[string]int{"input_tokens": 10, "output_tokens": 5},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// eventCapture records trap events POSTed to /api/proxy/events.
type eventCapture struct {
	mu     sync.Mutex
	events []map[string]interface{}
}

func (ec *eventCapture) list() []map[string]interface{} {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	cp := make([]map[string]interface{}, len(ec.events))
	copy(cp, ec.events)
	return cp
}

func (ec *eventCapture) waitFor(t *testing.T, n int, timeout time.Duration) []map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := ec.list(); len(got) >= n {
			return got
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d events (got %d)", n, len(ec.list()))
	return nil
}

func mockDashboardServer(t *testing.T, capture *eventCapture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/proxy/events" && r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			var ev map[string]interface{}
			_ = json.Unmarshal(body, &ev)
			capture.mu.Lock()
			capture.events = append(capture.events, ev)
			capture.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

// ---------------------------------------------------------------------------
// Test environment
// ---------------------------------------------------------------------------

type testEnv struct {
	proxyURL string
	engine   *trap.Engine
	events   *eventCapture
}

func findFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("findFreePort: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func waitForHealth(t *testing.T, url string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server at %s not ready within %s", url, timeout)
}

// setupEnv creates a full test environment with mock Anthropic, mock dashboard,
// and a real AgentsAegis proxy. When superDebug is true the proxy injects a
// canary trap on every bash command and disables hook jitter/cooldown.
func setupEnv(t *testing.T, superDebug bool) *testEnv {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".agentsaegis", "traps"), 0o700); err != nil {
		t.Fatal(err)
	}

	anthropic := mockAnthropicServer(t)
	t.Cleanup(anthropic.Close)

	capture := &eventCapture{}
	dashboard := mockDashboardServer(t, capture)
	t.Cleanup(dashboard.Close)

	port := findFreePort(t)

	canary := trap.CanaryTemplate()
	orgCfg := trap.OrgConfig{
		TrapFrequency:  1,
		MaxTrapsPerDay: 999,
		Categories:     []string{"debug_canary"},
	}
	engine := trap.NewEngine(orgCfg)
	if superDebug {
		engine.SetForceInject(true)
	}
	selector := trap.NewSelector([]*trap.Template{canary})

	apiClient := client.New(dashboard.URL, "e2e-token")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	cfg := &config.Config{
		ProxyPort:        port,
		AnthropicBaseURL: anthropic.URL,
	}
	srv := server.New(cfg, engine, selector, apiClient, logger)
	if superDebug {
		srv.SetSuperDebug()
	}

	go func() { _ = srv.Start() }()

	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForHealth(t, proxyURL+"/__aegis/health", 5*time.Second)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	return &testEnv{proxyURL: proxyURL, engine: engine, events: capture}
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

// sendChatRequest sends a simulated Claude Code /v1/messages request through the proxy.
func sendChatRequest(t *testing.T, proxyURL string) map[string]interface{} {
	t.Helper()
	body := `{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages": [{"role": "user", "content": "list files"}]
	}`
	req, err := http.NewRequest(http.MethodPost, proxyURL+"/v1/messages",
		bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "test-key")
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("proxy returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode proxy response: %v", err)
	}
	return result
}

// extractBashCommand extracts the bash command from a Claude API response.
func extractBashCommand(t *testing.T, resp map[string]interface{}) string {
	t.Helper()
	content, ok := resp["content"].([]interface{})
	if !ok {
		t.Fatal("response missing content array")
	}
	for _, block := range content {
		m, ok := block.(map[string]interface{})
		if !ok {
			continue
		}
		if m["type"] == "tool_use" && strings.EqualFold(fmt.Sprint(m["name"]), "bash") {
			input, ok := m["input"].(map[string]interface{})
			if !ok {
				t.Fatal("tool_use missing input")
			}
			cmd, ok := input["command"].(string)
			if !ok {
				t.Fatal("input missing command string")
			}
			return cmd
		}
	}
	t.Fatal("no bash tool_use block found in response")
	return ""
}

// sendHookRequest sends a PreToolUse hook request and returns the parsed response.
func sendHookRequest(t *testing.T, proxyURL, command string) map[string]interface{} {
	t.Helper()
	hookBody, _ := json.Marshal(map[string]interface{}{
		"session_id":      "e2e-session",
		"hook_event_name": "PreToolUse",
		"tool_name":       "Bash",
		"tool_input":      map[string]string{"command": command},
		"tool_use_id":     "toolu_e2e_001",
	})
	resp, err := http.Post(proxyURL+"/hooks/pre-tool-use", "application/json",
		bytes.NewReader(hookBody))
	if err != nil {
		t.Fatalf("hook request failed: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode hook response: %v", err)
	}
	return result
}

// trapFileCount returns the number of trap files in ~/.agentsaegis/traps/.
func trapFileCount(t *testing.T) int {
	t.Helper()
	dir, err := trap.TrapFileDir()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	count := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			count++
		}
	}
	return count
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestFullTrapLifecycle(t *testing.T) {
	env := setupEnv(t, true)

	// 1. Send a Claude Code request through the proxy
	resp := sendChatRequest(t, env.proxyURL)

	// 2. Assert: response contains a trap command, not the original
	cmd := extractBashCommand(t, resp)
	if cmd == "echo hello world" {
		t.Fatal("proxy did not inject a trap - command is still the original")
	}
	if !strings.Contains(cmd, "aegis_canary") {
		t.Fatalf("expected canary trap command, got: %s", cmd)
	}

	// 3. Assert: trap file was written
	if n := trapFileCount(t); n != 1 {
		t.Fatalf("expected 1 trap file, got %d", n)
	}

	// 4. Send the trap command to the hook endpoint (simulating developer approval)
	hookResp := sendHookRequest(t, env.proxyURL, cmd)

	// 5. Assert: hook returned deny
	output, ok := hookResp["hookSpecificOutput"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected hookSpecificOutput in response, got: %v", hookResp)
	}
	if output["permissionDecision"] != "deny" {
		t.Fatalf("expected deny, got: %v", output["permissionDecision"])
	}

	// 6. Assert: dashboard received the event (async, wait for it)
	events := env.events.waitFor(t, 1, 5*time.Second)
	ev := events[0]
	if ev["result"] != "missed" {
		t.Errorf("expected result=missed, got %v", ev["result"])
	}
	if ev["trap_template_id"] != "debug_canary" {
		t.Errorf("expected trap_template_id=debug_canary, got %v", ev["trap_template_id"])
	}
	if ev["original_command"] != "echo hello world" {
		t.Errorf("expected original_command='echo hello world', got %v", ev["original_command"])
	}

	// 7. Assert: trap file is cleaned up
	time.Sleep(100 * time.Millisecond) // small delay for file cleanup
	if n := trapFileCount(t); n != 0 {
		t.Fatalf("expected 0 trap files after resolution, got %d", n)
	}
}

func TestLegitCommandPassthrough(t *testing.T) {
	env := setupEnv(t, true)

	// Inject a trap via a proxy request
	resp := sendChatRequest(t, env.proxyURL)
	trapCmd := extractBashCommand(t, resp)
	if !strings.Contains(trapCmd, "aegis_canary") {
		t.Fatalf("trap was not injected: %s", trapCmd)
	}
	if n := trapFileCount(t); n != 1 {
		t.Fatalf("expected 1 trap file, got %d", n)
	}

	// Send a NON-trap command to the hook
	hookResp := sendHookRequest(t, env.proxyURL, "ls -la")

	// Assert: allowed through (empty response = allow)
	if _, hasDeny := hookResp["hookSpecificOutput"]; hasDeny {
		t.Fatalf("expected allow (no hookSpecificOutput), got: %v", hookResp)
	}

	// Assert: trap file still exists (not consumed by a non-matching command)
	if n := trapFileCount(t); n != 1 {
		t.Fatalf("expected 1 trap file still present, got %d", n)
	}
}

func TestTrapExpiry(t *testing.T) {
	env := setupEnv(t, true)

	// Inject a trap
	resp := sendChatRequest(t, env.proxyURL)
	trapCmd := extractBashCommand(t, resp)
	if !strings.Contains(trapCmd, "aegis_canary") {
		t.Fatalf("trap was not injected: %s", trapCmd)
	}

	// Simulate expiry by backdating the active trap's InjectedAt
	activeTrap := env.engine.GetActiveTrap()
	if activeTrap == nil {
		t.Fatal("expected an active trap after injection")
	}
	activeTrap.InjectedAt = time.Now().Add(-3 * time.Minute)

	// Send a non-matching command - hook should detect expiry and allow through
	hookResp := sendHookRequest(t, env.proxyURL, "git status")
	if _, hasDeny := hookResp["hookSpecificOutput"]; hasDeny {
		t.Fatalf("expected allow after expiry, got deny: %v", hookResp)
	}

	// Active trap should be cleared after expiry
	if at := env.engine.GetActiveTrap(); at != nil {
		t.Fatal("expected active trap to be cleared after expiry")
	}

	// Dashboard should have received an event with result mapped to "missed"
	events := env.events.waitFor(t, 1, 5*time.Second)
	if events[0]["result"] != "missed" {
		t.Errorf("expected expired event result=missed, got %v", events[0]["result"])
	}
}

func TestProxyDownFailover(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not available")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	// Use a port that nothing is listening on
	unusedPort := findFreePort(t)

	wrapper := fmt.Sprintf(`
claude() {
  if curl -sf --max-time 1 http://localhost:%d/__aegis/health > /dev/null 2>&1; then
    echo "PROXY_ROUTE"
  else
    echo "DIRECT_ROUTE"
  fi
}
claude
`, unusedPort)

	cmd := exec.Command("bash", "-c", wrapper)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("wrapper execution failed: %v", err)
	}
	got := strings.TrimSpace(string(output))
	if got != "DIRECT_ROUTE" {
		t.Errorf("expected DIRECT_ROUTE (proxy down fallback), got %q", got)
	}
}

func TestHookDenyFormat(t *testing.T) {
	env := setupEnv(t, true)

	// Inject a trap
	resp := sendChatRequest(t, env.proxyURL)
	trapCmd := extractBashCommand(t, resp)

	// Send the trap command to the hook
	hookBody, _ := json.Marshal(map[string]interface{}{
		"session_id":      "e2e-session",
		"hook_event_name": "PreToolUse",
		"tool_name":       "Bash",
		"tool_input":      map[string]string{"command": trapCmd},
		"tool_use_id":     "toolu_e2e_001",
	})
	httpResp, err := http.Post(env.proxyURL+"/hooks/pre-tool-use",
		"application/json", bytes.NewReader(hookBody))
	if err != nil {
		t.Fatal(err)
	}
	defer httpResp.Body.Close()

	// Must be 200 OK (Claude Code ignores non-200)
	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", httpResp.StatusCode)
	}

	// Must be valid JSON
	rawBody, _ := io.ReadAll(httpResp.Body)
	var parsed map[string]interface{}
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		t.Fatalf("response is not valid JSON: %s", string(rawBody))
	}

	// Must have hookSpecificOutput at top level
	output, ok := parsed["hookSpecificOutput"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing or wrong type for hookSpecificOutput: %s", string(rawBody))
	}

	// Validate required fields per Claude Code hook spec
	if output["hookEventName"] != "PreToolUse" {
		t.Errorf("hookEventName = %v, want PreToolUse", output["hookEventName"])
	}
	if output["permissionDecision"] != "deny" {
		t.Errorf("permissionDecision = %v, want deny", output["permissionDecision"])
	}
	reason, ok := output["permissionDecisionReason"].(string)
	if !ok || reason == "" {
		t.Errorf("permissionDecisionReason missing or empty: %v", output["permissionDecisionReason"])
	}

	// Must not have unexpected top-level keys
	for key := range parsed {
		if key != "hookSpecificOutput" {
			t.Errorf("unexpected top-level key in deny response: %q", key)
		}
	}
}

func TestConcurrentRequests(t *testing.T) {
	// Use normal mode (no force inject) with frequency=1.
	// Only one trap should be injected; the rest pass through because
	// the engine blocks injection while an active trap exists.
	env := setupEnv(t, false)

	const n = 10
	type result struct {
		cmd string
		err error
	}
	results := make(chan result, n)

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			resp := sendChatRequest(t, env.proxyURL)
			cmd := extractBashCommand(t, resp)
			results <- result{cmd: cmd}
		}()
	}
	wg.Wait()
	close(results)

	trapped := 0
	passthrough := 0
	for r := range results {
		if r.err != nil {
			t.Errorf("request failed: %v", r.err)
			continue
		}
		if strings.Contains(r.cmd, "aegis_canary") {
			trapped++
		} else if r.cmd == "echo hello world" {
			passthrough++
		} else {
			t.Errorf("unexpected command in response: %s", r.cmd)
		}
	}

	if trapped != 1 {
		t.Errorf("expected exactly 1 trapped request, got %d", trapped)
	}
	if passthrough != n-1 {
		t.Errorf("expected %d passthrough requests, got %d", n-1, passthrough)
	}
	t.Logf("results: %d trapped, %d passthrough", trapped, passthrough)
}

func TestTrapCommandsAreHarmless(t *testing.T) {
	templates, err := trap.LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	if len(templates) == 0 {
		t.Fatal("no templates loaded")
	}
	t.Logf("loaded %d trap templates", len(templates))

	// Run safety validation on all templates
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	safe := trap.ValidateTrapSafety(templates, logger)

	if len(safe) != len(templates) {
		var rejected []string
		safeIDs := make(map[string]bool)
		for _, s := range safe {
			safeIDs[s.ID] = true
		}
		for _, tmpl := range templates {
			if !safeIDs[tmpl.ID] {
				rejected = append(rejected, tmpl.ID)
			}
		}
		t.Fatalf("%d/%d templates failed safety validation: %v",
			len(templates)-len(safe), len(templates), rejected)
	}

	// Verify each template has required fields
	for _, tmpl := range templates {
		if tmpl.ID == "" {
			t.Error("template with empty ID")
		}
		if tmpl.Category == "" {
			t.Errorf("template %s: empty category", tmpl.ID)
		}
		if tmpl.Severity == "" {
			t.Errorf("template %s: empty severity", tmpl.ID)
		}
		if len(tmpl.TrapCommands) == 0 {
			t.Errorf("template %s: no trap commands", tmpl.ID)
		}
		// Verify no trap command targets real user paths
		for _, cmd := range tmpl.TrapCommands {
			if strings.Contains(cmd, "$HOME") || strings.Contains(cmd, "~/") {
				t.Errorf("template %s: trap command references real home dir: %s", tmpl.ID, cmd)
			}
		}
	}

	// Verify template count matches expected (15 real + we test them all)
	var templateCount int32
	atomic.StoreInt32(&templateCount, int32(len(templates)))
	if atomic.LoadInt32(&templateCount) < 15 {
		t.Errorf("expected at least 15 templates, got %d", len(templates))
	}

	// Additional safety checks from spec
	for _, tmpl := range templates {
		for _, cmd := range tmpl.TrapCommands {
			// Commands with --dry-run are inherently safe, skip detailed checks
			isDryRun := strings.Contains(cmd, "--dry-run")

			if !isDryRun {
				// No bare relative paths without safe prefix
				if strings.Contains(cmd, " .git/") && !strings.Contains(cmd, "aegis") && !strings.Contains(cmd, "/tmp/") && !strings.Contains(cmd, "nonexistent") {
					t.Errorf("template %s: trap command references bare .git/ path: %s", tmpl.ID, cmd)
				}
				// No real env var expansion in double quotes
				for _, dangerousVar := range []string{`"$HOME"`, `"$USER"`, `"$PATH"`, `"$SSH_`} {
					if strings.Contains(cmd, dangerousVar) {
						t.Errorf("template %s: trap command expands real env var: %s", tmpl.ID, cmd)
					}
				}
			}
			// No real npm/pip package names (must use aegis-trap prefix or nonexistent marker)
			if (strings.HasPrefix(cmd, "npm install") || strings.HasPrefix(cmd, "pip install")) &&
				!strings.Contains(cmd, "aegis-trap") && !strings.Contains(cmd, "nonexistent") {
				t.Errorf("template %s: trap command installs real package: %s", tmpl.ID, cmd)
			}
			// No real Docker containers (docker run/exec, not filenames containing "docker")
			if (strings.Contains(cmd, "docker run") || strings.Contains(cmd, "docker exec")) &&
				!strings.Contains(cmd, "aegis") && !strings.Contains(cmd, "nonexistent") {
				t.Errorf("template %s: trap command uses real Docker image: %s", tmpl.ID, cmd)
			}
			// No real git remotes (must use aegis-nonexistent or fake marker)
			if strings.Contains(cmd, "git push") &&
				!strings.Contains(cmd, "nonexistent") && !strings.Contains(cmd, "aegis") {
				t.Errorf("template %s: trap command uses real git remote: %s", tmpl.ID, cmd)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Test 8: Proxy health endpoint
// ---------------------------------------------------------------------------

func TestProxyHealthEndpoint(t *testing.T) {
	env := setupEnv(t, false)

	resp, err := http.Get(env.proxyURL + "/__aegis/health")
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode health response: %v", err)
	}

	if body["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", body["status"])
	}
}

// ---------------------------------------------------------------------------
// Test 9: Hook allows when no trap active
// ---------------------------------------------------------------------------

func TestHookAllowsWhenNoTrap(t *testing.T) {
	env := setupEnv(t, false)

	// No chat request sent - no trap injected.
	// Send any command to the hook.
	hookResp := sendHookRequest(t, env.proxyURL, "echo hello")

	// Assert: allowed through (no hookSpecificOutput)
	if _, hasDeny := hookResp["hookSpecificOutput"]; hasDeny {
		t.Fatalf("expected allow (no active trap), got deny: %v", hookResp)
	}

	// Verify no trap files exist
	if n := trapFileCount(t); n != 0 {
		t.Fatalf("expected 0 trap files, got %d", n)
	}
}

// ---------------------------------------------------------------------------
// Test 10: Proxy passes through non-bash requests
// ---------------------------------------------------------------------------

// mockAnthropicTextOnly returns a server that responds with a text-only message
// (no bash tool_use block).
func mockAnthropicTextOnly(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		resp := map[string]interface{}{
			"id": "msg_text", "type": "message", "role": "assistant",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "The answer is 42."},
			},
			"model": "claude-sonnet-4-20250514", "stop_reason": "end_turn",
			"usage": map[string]int{"input_tokens": 10, "output_tokens": 5},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestNonBashPassthrough(t *testing.T) {
	// Custom setup with text-only mock Anthropic
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".agentsaegis", "traps"), 0o700); err != nil {
		t.Fatal(err)
	}

	anthropic := mockAnthropicTextOnly(t)
	t.Cleanup(anthropic.Close)

	capture := &eventCapture{}
	dashboard := mockDashboardServer(t, capture)
	t.Cleanup(dashboard.Close)

	port := findFreePort(t)
	canary := trap.CanaryTemplate()
	orgCfg := trap.OrgConfig{TrapFrequency: 1, MaxTrapsPerDay: 999, Categories: []string{"debug_canary"}}
	engine := trap.NewEngine(orgCfg)
	engine.SetForceInject(true) // Force inject so we KNOW it would inject if bash was present
	selector := trap.NewSelector([]*trap.Template{canary})
	apiClient := client.New(dashboard.URL, "e2e-token")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	cfg := &config.Config{ProxyPort: port, AnthropicBaseURL: anthropic.URL}
	srv := server.New(cfg, engine, selector, apiClient, logger)
	srv.SetSuperDebug()

	go func() { _ = srv.Start() }()
	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForHealth(t, proxyURL+"/__aegis/health", 5*time.Second)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	// Send request through proxy
	body := `{"model":"claude-sonnet-4-20250514","max_tokens":1024,"messages":[{"role":"user","content":"what is 6*7?"}]}`
	req, err := http.NewRequest(http.MethodPost, proxyURL+"/v1/messages",
		bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "test-key")
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Verify response is unmodified - should have text block, no tool_use
	content, ok := result["content"].([]interface{})
	if !ok {
		t.Fatal("response missing content array")
	}
	if len(content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(content))
	}
	block := content[0].(map[string]interface{})
	if block["type"] != "text" {
		t.Fatalf("expected text block, got %v", block["type"])
	}
	if block["text"] != "The answer is 42." {
		t.Fatalf("response text was modified: %v", block["text"])
	}

	// No trap should have been injected
	if n := trapFileCount(t); n != 0 {
		t.Fatalf("expected 0 trap files for non-bash response, got %d", n)
	}
}

// ---------------------------------------------------------------------------
// Test 11: Super-debug mode
// ---------------------------------------------------------------------------

func TestSuperDebugMode(t *testing.T) {
	env := setupEnv(t, true)

	const n = 5
	for i := 0; i < n; i++ {
		resp := sendChatRequest(t, env.proxyURL)
		cmd := extractBashCommand(t, resp)
		if !strings.Contains(cmd, "aegis_canary") {
			t.Fatalf("request %d: expected canary trap, got: %s", i+1, cmd)
		}

		// Resolve the trap so next request can inject again
		hookResp := sendHookRequest(t, env.proxyURL, cmd)
		if output, ok := hookResp["hookSpecificOutput"].(map[string]interface{}); ok {
			if output["permissionDecision"] != "deny" {
				t.Fatalf("request %d: expected deny, got: %v", i+1, output["permissionDecision"])
			}
		} else {
			t.Fatalf("request %d: expected deny response, got allow", i+1)
		}

		// Small delay for async cleanup
		time.Sleep(50 * time.Millisecond)
	}

	// All 5 iterations should have generated dashboard events
	events := env.events.waitFor(t, n, 10*time.Second)
	for i, ev := range events {
		if ev["result"] != "missed" {
			t.Errorf("event %d: expected result=missed, got %v", i, ev["result"])
		}
	}
}

// ---------------------------------------------------------------------------
// Test 12: Daemon start/stop/status
// ---------------------------------------------------------------------------

func TestDaemonStartStopStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("daemon test requires subprocess execution")
	}

	// Build the binary
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "agentsaegis")
	buildCmd := exec.Command("go", "build", "-o", binPath, "./cmd/agentsaegis")
	buildCmd.Dir = ".."
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	// Set up isolated HOME with config
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".agentsaegis")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Find a free port for the proxy
	port := findFreePort(t)

	// Write minimal config
	configContent := fmt.Sprintf("proxy_port: %d\nlog_level: info\n", port)
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configContent), 0o600); err != nil {
		t.Fatal(err)
	}

	// Start daemon
	startCmd := exec.Command(binPath, "start", "--daemon")
	startCmd.Env = append(os.Environ(), "HOME="+home)
	startOut, err := startCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("start --daemon failed: %v\n%s", err, startOut)
	}

	// Wait for the daemon to be ready
	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForHealth(t, proxyURL+"/__aegis/health", 10*time.Second)

	// Verify PID file exists
	pidPath := filepath.Join(configDir, "aegis.pid")
	if _, err := os.Stat(pidPath); os.IsNotExist(err) {
		t.Fatal("PID file does not exist after start --daemon")
	}

	// Status should show running
	statusCmd := exec.Command(binPath, "status")
	statusCmd.Env = append(os.Environ(), "HOME="+home)
	statusOut, err := statusCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status failed: %v\n%s", err, statusOut)
	}
	if !strings.Contains(string(statusOut), "RUNNING") {
		t.Fatalf("expected RUNNING in status output, got:\n%s", statusOut)
	}

	// Stop daemon
	stopCmd := exec.Command(binPath, "stop")
	stopCmd.Env = append(os.Environ(), "HOME="+home)
	stopOut, err := stopCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("stop failed: %v\n%s", err, stopOut)
	}

	// Wait briefly for process cleanup
	time.Sleep(500 * time.Millisecond)

	// Status should show stopped
	statusCmd2 := exec.Command(binPath, "status")
	statusCmd2.Env = append(os.Environ(), "HOME="+home)
	statusOut2, err := statusCmd2.CombinedOutput()
	if err != nil {
		// status may exit non-zero when config can't load - check output
		t.Logf("status after stop: %v\n%s", err, statusOut2)
	}
	if strings.Contains(string(statusOut2), "RUNNING") {
		t.Fatalf("expected STOPPED after stop, got:\n%s", statusOut2)
	}

	// PID file should be cleaned up
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("PID file still exists after stop")
	}
}
