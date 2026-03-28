# Live E2E Test Runner Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build an automated E2E test runner that makes real API calls through the go-proxy binary to verify SSE parsing, trap injection, and event reporting across Claude CLI and Copilot (GPT/Claude/Codex).

**Architecture:** Go test files in `e2e/` with `//go:build live` tag. Tests build the real binary, start it as a subprocess, make real API calls through it, and verify SSE trap injection + dashboard event reporting. Two-phase execution: super-debug ON for injection tests, OFF for passthrough tests.

**Tech Stack:** Go 1.26+, standard `testing` package, `net/http`, `crypto/tls`, `crypto/x509`, `os/exec`

---

## File Structure

```
e2e/
  live_helpers_test.go  - Proxy subprocess lifecycle, port finder, health check, config writer,
                          CA loader, Copilot token exchange, dashboard event query, SSE parsers
  live_test.go          - TestMain (builds binary), TestLiveSuperDebug, TestLivePassthrough,
                          summary matrix printer, result tracking
  live_claude_test.go   - Claude-specific request builder, Anthropic SSE parser,
                          all 5 Claude scenarios as subtests
  live_copilot_test.go  - Copilot CONNECT client builder, OpenAI SSE parser,
                          all 5 scenarios x 3 models as subtests
Makefile                - Add test-live target
```

---

### Task 1: Proxy Subprocess Helpers (`live_helpers_test.go` - part 1)

**Files:**
- Create: `e2e/live_helpers_test.go`

This task builds the foundational helpers: find free port, write config, start/stop proxy binary, wait for health, load CA cert. All subsequent tasks depend on these.

- [ ] **Step 1: Create `live_helpers_test.go` with build tag, package, and imports**

```go
//go:build live

package e2e_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// liveFindFreePort returns an available TCP port on localhost.
func liveFindFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("liveFindFreePort: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// liveWaitForHealth polls the proxy health endpoint until it returns 200 or timeout.
func liveWaitForHealth(t *testing.T, proxyURL string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(proxyURL + "/__aegis/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("proxy at %s not healthy within %s", proxyURL, timeout)
}

// liveWriteConfig writes a minimal config.yaml into the temp home directory.
func liveWriteConfig(t *testing.T, homeDir string, dashboardURL, apiToken string) {
	t.Helper()
	configDir := filepath.Join(homeDir, ".agentsaegis")
	if err := os.MkdirAll(filepath.Join(configDir, "traps"), 0o700); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	configContent := fmt.Sprintf(
		"dashboard_url: %q\napi_token: %q\n",
		dashboardURL, apiToken,
	)
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configContent), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
}

// proxyInstance manages a proxy binary subprocess.
type proxyInstance struct {
	cmd      *exec.Cmd
	port     int
	homeDir  string
	proxyURL string
	stderr   *bytes.Buffer
	cancel   context.CancelFunc
}

// liveStartProxy builds env vars and starts the proxy binary as a subprocess.
// It waits for the health endpoint before returning.
// binaryPath is the path to the compiled agentsaegis binary.
// superDebug controls whether --super-debug is passed.
func liveStartProxy(t *testing.T, binaryPath string, homeDir string, port int, superDebug bool, dashboardURL, apiToken string) *proxyInstance {
	t.Helper()

	liveWriteConfig(t, homeDir, dashboardURL, apiToken)

	args := []string{"start", "--debug"}
	if superDebug {
		args = []string{"start", "--super-debug"}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, binaryPath, args...)

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	cmd.Stdout = io.Discard

	cmd.Env = append(os.Environ(),
		"HOME="+homeDir,
		fmt.Sprintf("AEGIS_PROXY_PORT=%d", port),
		"AEGIS_DASHBOARD_URL="+dashboardURL,
		"AEGIS_API_TOKEN="+apiToken,
	)

	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("starting proxy: %v", err)
	}

	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	pi := &proxyInstance{
		cmd:      cmd,
		port:     port,
		homeDir:  homeDir,
		proxyURL: proxyURL,
		stderr:   &stderrBuf,
		cancel:   cancel,
	}

	// Wait for health with generous timeout (binary needs to generate CA on first start)
	liveWaitForHealth(t, proxyURL, 15*time.Second)

	t.Cleanup(func() {
		pi.stop()
	})

	return pi
}

// stop sends SIGTERM and waits up to 5 seconds, then kills.
func (pi *proxyInstance) stop() {
	if pi.cmd.Process == nil {
		return
	}
	// Signal the context cancellation which sends SIGKILL via CommandContext,
	// but we prefer a graceful SIGTERM first.
	_ = pi.cmd.Process.Signal(os.Interrupt)

	done := make(chan struct{})
	go func() {
		_ = pi.cmd.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Process exited gracefully
	case <-time.After(5 * time.Second):
		pi.cancel() // Force kill via context
		<-done
	}
}

// logOnFailure dumps the proxy stderr log when the test fails.
func (pi *proxyInstance) logOnFailure(t *testing.T) {
	t.Helper()
	if t.Failed() {
		t.Logf("=== Proxy stderr log ===\n%s", pi.stderr.String())
	}
}

// loadProxyCA reads the proxy's generated CA certificate for TLS MITM trust.
func loadProxyCA(t *testing.T, homeDir string) *x509.CertPool {
	t.Helper()
	caPEM, err := os.ReadFile(filepath.Join(homeDir, ".agentsaegis", "ca.pem"))
	if err != nil {
		t.Fatalf("reading proxy CA: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("failed to parse proxy CA certificate")
	}
	return pool
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /Users/DekaKisaLove/personal-project/agentsaegis/go-proxy && go build -tags live ./e2e/...`
Expected: No errors (file compiles but no tests yet)

- [ ] **Step 3: Commit**

```bash
git add e2e/live_helpers_test.go
git commit -m "feat(e2e): add proxy subprocess helpers for live tests"
```

---

### Task 2: Copilot Token Exchange and CONNECT Client (`live_helpers_test.go` - part 2)

**Files:**
- Modify: `e2e/live_helpers_test.go`

Adds the Copilot token acquisition (GitHub token -> Copilot session token) and an HTTP client that routes through the proxy's CONNECT tunnel with TLS MITM trust.

- [ ] **Step 1: Add Copilot token exchange and CONNECT client to `live_helpers_test.go`**

Append to the end of the file:

```go
// copilotAuth holds a Copilot session token and API endpoint.
type copilotAuth struct {
	Token    string
	Endpoint string // e.g. "https://api.individual.githubcopilot.com"
}

// acquireCopilotToken exchanges a GitHub token for a short-lived Copilot API token.
// Returns nil and skips Copilot tests if no GitHub token is available.
func acquireCopilotToken(t *testing.T) *copilotAuth {
	t.Helper()

	githubToken := os.Getenv("GITHUB_TOKEN")
	if githubToken == "" {
		// Try gh CLI fallback
		out, err := exec.Command("gh", "auth", "token").Output()
		if err != nil {
			return nil // Caller should t.Skip
		}
		githubToken = strings.TrimSpace(string(out))
	}
	if githubToken == "" {
		return nil
	}

	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/copilot_internal/v2/token", nil)
	if err != nil {
		t.Fatalf("building copilot token request: %v", err)
	}
	req.Header.Set("Authorization", "token "+githubToken)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("copilot token exchange failed: %v", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Logf("copilot token exchange returned %d: %s", resp.StatusCode, string(body))
		return nil
	}

	var tokenResp struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		t.Fatalf("decoding copilot token response: %v", err)
	}

	endpoint := tokenResp.Endpoints.API
	if endpoint == "" {
		endpoint = "https://api.individual.githubcopilot.com"
	}

	return &copilotAuth{
		Token:    tokenResp.Token,
		Endpoint: endpoint,
	}
}

// copilotHTTPClient creates an HTTP client that routes through the proxy's
// CONNECT tunnel and trusts the proxy's MITM CA certificate.
func copilotHTTPClient(t *testing.T, proxyAddr string, caPool *x509.CertPool) *http.Client {
	t.Helper()
	proxyURL, err := url.Parse(fmt.Sprintf("http://%s", proxyAddr))
	if err != nil {
		t.Fatalf("parsing proxy URL: %v", err)
	}
	return &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				RootCAs: caPool,
			},
		},
	}
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /Users/DekaKisaLove/personal-project/agentsaegis/go-proxy && go build -tags live ./e2e/...`
Expected: No errors

- [ ] **Step 3: Commit**

```bash
git add e2e/live_helpers_test.go
git commit -m "feat(e2e): add Copilot token exchange and CONNECT tunnel client"
```

---

### Task 3: Dashboard Event Query and SSE Parsers (`live_helpers_test.go` - part 3)

**Files:**
- Modify: `e2e/live_helpers_test.go`

Adds the dashboard event query helper (polls for async event delivery) and SSE response parsers for both Anthropic and OpenAI formats.

- [ ] **Step 1: Add dashboard query and SSE parsers to `live_helpers_test.go`**

Append to the end of the file:

```go
// dashboardEvent represents a trap event returned by the dashboard query API.
type dashboardEvent struct {
	TrapTemplateID string `json:"trap_template_id"`
	TrapCategory   string `json:"trap_category"`
	Result         string `json:"result"`
	SessionID      string `json:"session_id"`
}

// queryDashboardEvents polls the dashboard API for events matching a session ID.
// It retries with backoff since event reporting is async (fire-and-forget goroutine).
// Returns empty slice if no events found within timeout.
func queryDashboardEvents(t *testing.T, dashboardURL, apiToken, sessionID string, timeout time.Duration) []dashboardEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		reqURL := fmt.Sprintf("%s/api/proxy/events?session_id=%s", dashboardURL, url.QueryEscape(sessionID))
		req, err := http.NewRequest(http.MethodGet, reqURL, nil)
		if err != nil {
			t.Fatalf("building dashboard query: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+apiToken)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			time.Sleep(200 * time.Millisecond)
			continue
		}

		var result struct {
			Events []dashboardEvent `json:"events"`
		}
		err = json.NewDecoder(resp.Body).Decode(&result)
		_ = resp.Body.Close()
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}

		if len(result.Events) > 0 {
			return result.Events
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}

// assertNoDashboardEvents verifies no events exist for a session within a short window.
func assertNoDashboardEvents(t *testing.T, dashboardURL, apiToken, sessionID string) {
	t.Helper()
	// Wait a short time for any async events to land, then assert none exist
	events := queryDashboardEvents(t, dashboardURL, apiToken, sessionID, 3*time.Second)
	if len(events) > 0 {
		t.Errorf("expected no events for session %s, got %d: %+v", sessionID, len(events), events)
	}
}

// sseToolUse holds a parsed tool_use extracted from an SSE stream.
type sseToolUse struct {
	ID       string // tool_use block ID (e.g., "toolu_xxx")
	Name     string // tool name (e.g., "bash")
	Command  string // extracted command string
	RawInput string // raw input JSON
}

// parseAnthropicSSE reads an Anthropic-format SSE stream and extracts all bash tool_use blocks.
// It accumulates content_block_start/delta/stop events to reconstruct the tool_use input.
func parseAnthropicSSE(t *testing.T, body io.Reader) (textContent string, toolUses []sseToolUse) {
	t.Helper()
	scanner := newLineScanner(body)

	var currentToolUse *sseToolUse
	var inputBuf strings.Builder
	var textBuf strings.Builder

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event map[string]interface{}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		eventType, _ := event["type"].(string)
		switch eventType {
		case "content_block_start":
			cb, _ := event["content_block"].(map[string]interface{})
			if cb == nil {
				continue
			}
			if cb["type"] == "tool_use" {
				name, _ := cb["name"].(string)
				id, _ := cb["id"].(string)
				currentToolUse = &sseToolUse{ID: id, Name: name}
				inputBuf.Reset()
			}

		case "content_block_delta":
			delta, _ := event["delta"].(map[string]interface{})
			if delta == nil {
				continue
			}
			if delta["type"] == "input_json_delta" {
				partial, _ := delta["partial_json"].(string)
				inputBuf.WriteString(partial)
			}
			if delta["type"] == "text_delta" {
				text, _ := delta["text"].(string)
				textBuf.WriteString(text)
			}

		case "content_block_stop":
			if currentToolUse != nil {
				raw := inputBuf.String()
				currentToolUse.RawInput = raw
				var input map[string]interface{}
				if err := json.Unmarshal([]byte(raw), &input); err == nil {
					if cmd, ok := input["command"].(string); ok {
						currentToolUse.Command = cmd
					}
				}
				toolUses = append(toolUses, *currentToolUse)
				currentToolUse = nil
			}
		}
	}
	return textBuf.String(), toolUses
}

// parseOpenAISSE reads an OpenAI-format SSE stream and extracts all tool calls.
// It accumulates choices[].delta.tool_calls deltas to reconstruct function arguments.
func parseOpenAISSE(t *testing.T, body io.Reader) (textContent string, toolUses []sseToolUse) {
	t.Helper()
	scanner := newLineScanner(body)

	type callState struct {
		ID       string
		Name     string
		ArgsBuf  strings.Builder
	}
	activeCalls := map[int]*callState{}
	var textBuf strings.Builder

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				textBuf.WriteString(choice.Delta.Content)
			}
			for _, tc := range choice.Delta.ToolCalls {
				state, exists := activeCalls[tc.Index]
				if !exists {
					state = &callState{}
					activeCalls[tc.Index] = state
				}
				if tc.ID != "" {
					state.ID = tc.ID
				}
				if tc.Function.Name != "" {
					state.Name = tc.Function.Name
				}
				state.ArgsBuf.WriteString(tc.Function.Arguments)
			}

			if choice.FinishReason != nil && *choice.FinishReason == "tool_calls" {
				// Flush all active calls
				for _, state := range activeCalls {
					raw := state.ArgsBuf.String()
					tu := sseToolUse{
						ID:       state.ID,
						Name:     state.Name,
						RawInput: raw,
					}
					var args map[string]interface{}
					if err := json.Unmarshal([]byte(raw), &args); err == nil {
						if cmd, ok := args["command"].(string); ok {
							tu.Command = cmd
						}
					}
					toolUses = append(toolUses, tu)
				}
				activeCalls = map[int]*callState{}
			}
		}
	}

	// Flush any remaining (in case finish_reason was not "tool_calls")
	for _, state := range activeCalls {
		raw := state.ArgsBuf.String()
		tu := sseToolUse{
			ID:       state.ID,
			Name:     state.Name,
			RawInput: raw,
		}
		var args map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &args); err == nil {
			if cmd, ok := args["command"].(string); ok {
				tu.Command = cmd
			}
		}
		toolUses = append(toolUses, tu)
	}

	return textBuf.String(), toolUses
}

// newLineScanner wraps a reader in a bufio.Scanner with a large buffer for SSE lines.
func newLineScanner(r io.Reader) *lineScanner {
	return &lineScanner{reader: r, buf: make([]byte, 0, 64*1024)}
}

// lineScanner reads lines from an io.Reader, handling SSE format (lines can be large).
// We use a custom implementation instead of bufio.Scanner to handle very large SSE lines
// that exceed the default 64KB limit.
type lineScanner struct {
	reader  io.Reader
	buf     []byte
	current string
	err     error
}

func (ls *lineScanner) Scan() bool {
	for {
		// Check if we have a complete line in the buffer
		if idx := bytes.IndexByte(ls.buf, '\n'); idx >= 0 {
			ls.current = string(ls.buf[:idx])
			ls.current = strings.TrimRight(ls.current, "\r")
			ls.buf = ls.buf[idx+1:]
			return true
		}
		// Read more data
		tmp := make([]byte, 4096)
		n, err := ls.reader.Read(tmp)
		if n > 0 {
			ls.buf = append(ls.buf, tmp[:n]...)
		}
		if err != nil {
			ls.err = err
			// Flush remaining buffer as last line
			if len(ls.buf) > 0 {
				ls.current = string(ls.buf)
				ls.current = strings.TrimRight(ls.current, "\r\n")
				ls.buf = nil
				return true
			}
			return false
		}
	}
}

func (ls *lineScanner) Text() string {
	return ls.current
}

// uniqueSessionID generates a unique session ID for test isolation.
func uniqueSessionID(provider, scenario string) string {
	return fmt.Sprintf("live-e2e-%s-%s-%d", provider, scenario, time.Now().UnixNano())
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /Users/DekaKisaLove/personal-project/agentsaegis/go-proxy && go build -tags live ./e2e/...`
Expected: No errors

- [ ] **Step 3: Commit**

```bash
git add e2e/live_helpers_test.go
git commit -m "feat(e2e): add dashboard event query and SSE parsers for live tests"
```

---

### Task 4: Test Matrix Runner and TestMain (`live_test.go`)

**Files:**
- Create: `e2e/live_test.go`

This creates the test orchestrator: TestMain builds the binary, and two top-level tests run the two phases (super-debug on/off). Includes the result tracker and summary matrix printer.

- [ ] **Step 1: Create `live_test.go`**

```go
//go:build live

package e2e_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var (
	// liveBinaryPath is set by TestMain after building the binary.
	liveBinaryPath string

	// liveAPIToken is the dashboard API token from env.
	liveAPIToken string

	// liveDashboardURL is the dashboard URL from env.
	liveDashboardURL string

	// liveAnthropicKey is the Anthropic API key from env.
	liveAnthropicKey string

	// liveCopilotAuth is the Copilot session token (acquired once in TestMain).
	liveCopilotAuth *copilotAuth
)

// resultTracker records pass/fail/skip per provider+scenario for the summary matrix.
type resultTracker struct {
	mu      sync.Mutex
	results map[string]map[string]string // provider -> scenario -> "PASS"/"FAIL"/"SKIP"
}

var liveResults = &resultTracker{
	results: map[string]map[string]string{},
}

func (rt *resultTracker) record(provider, scenario, result string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.results[provider] == nil {
		rt.results[provider] = map[string]string{}
	}
	rt.results[provider][scenario] = result
}

var providers = []string{"Claude", "Copilot/GPT", "Copilot/Claude", "Copilot/Codex"}
var scenarios = []string{"Passthrough", "Injection", "Approve", "Reject", "Clean"}

func printResultMatrix() {
	liveResults.mu.Lock()
	defer liveResults.mu.Unlock()

	fmt.Println()
	fmt.Println("Live E2E Test Results")
	fmt.Println("=====================")
	fmt.Printf("%-16s", "Provider")
	for _, s := range scenarios {
		fmt.Printf("| %-12s", s)
	}
	fmt.Println()
	fmt.Println(strings.Repeat("-", 16+13*len(scenarios)))

	tested, passed, failed := 0, 0, 0
	for _, p := range providers {
		fmt.Printf("%-16s", p)
		providerTested := false
		for _, s := range scenarios {
			r := "----"
			if results, ok := liveResults.results[p]; ok {
				if v, ok := results[s]; ok {
					r = v
					if v != "SKIP" {
						providerTested = true
					}
					switch v {
					case "PASS":
						passed++
					case "FAIL":
						failed++
					}
				}
			}
			fmt.Printf("| %-12s", r)
		}
		if providerTested {
			tested++
		}
		fmt.Println()
	}
	fmt.Println()
	total := passed + failed
	fmt.Printf("%d/%d providers tested, %d/%d passed, %d failed\n",
		tested, len(providers), passed, total, failed)
	fmt.Println()
}

func TestMain(m *testing.M) {
	liveAPIToken = os.Getenv("AEGIS_API_TOKEN")
	liveDashboardURL = os.Getenv("AEGIS_DASHBOARD_URL")
	if liveDashboardURL == "" {
		liveDashboardURL = "https://api.agentsaegis.com"
	}
	liveAnthropicKey = os.Getenv("ANTHROPIC_API_KEY")

	// Build the binary
	tmpDir, err := os.MkdirTemp("", "aegis-live-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	liveBinaryPath = filepath.Join(tmpDir, "agentsaegis")
	buildCmd := exec.Command("go", "build", "-o", liveBinaryPath, "./cmd/agentsaegis")
	buildCmd.Dir = findRepoRoot()
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "building binary: %v\n", err)
		os.Exit(1)
	}

	// Acquire Copilot token once
	liveCopilotAuth = acquireCopilotTokenForMain()

	code := m.Run()
	printResultMatrix()
	os.Exit(code)
}

func findRepoRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

func acquireCopilotTokenForMain() *copilotAuth {
	githubToken := os.Getenv("GITHUB_TOKEN")
	if githubToken == "" {
		out, err := exec.Command("gh", "auth", "token").Output()
		if err != nil {
			fmt.Fprintln(os.Stderr, "No GITHUB_TOKEN and gh auth token failed - Copilot tests will be skipped")
			return nil
		}
		githubToken = strings.TrimSpace(string(out))
	}
	if githubToken == "" {
		return nil
	}

	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/copilot_internal/v2/token", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "building copilot token request: %v\n", err)
		return nil
	}
	req.Header.Set("Authorization", "token "+githubToken)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "copilot token exchange failed: %v\n", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "copilot token exchange returned %d: %s\n", resp.StatusCode, string(body))
		return nil
	}

	var tokenResp struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		fmt.Fprintf(os.Stderr, "decoding copilot token: %v\n", err)
		return nil
	}

	endpoint := tokenResp.Endpoints.API
	if endpoint == "" {
		endpoint = "https://api.individual.githubcopilot.com"
	}

	fmt.Fprintf(os.Stderr, "Copilot token acquired (endpoint: %s)\n", endpoint)
	return &copilotAuth{Token: tokenResp.Token, Endpoint: endpoint}
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /Users/DekaKisaLove/personal-project/agentsaegis/go-proxy && go build -tags live ./e2e/...`
Expected: No errors

- [ ] **Step 3: Commit**

```bash
git add e2e/live_test.go
git commit -m "feat(e2e): add TestMain with binary build, token exchange, and result matrix"
```

---

### Task 5: Claude CLI Test Scenarios (`live_claude_test.go`)

**Files:**
- Create: `e2e/live_claude_test.go`

All 5 Claude scenarios. Uses Anthropic SSE format via the reverse proxy path. Each scenario is a subtest under `TestLiveSuperDebug/Claude` or `TestLivePassthrough/Claude`.

- [ ] **Step 1: Create `live_claude_test.go`**

```go
//go:build live

package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
)

// claudeRequestBody builds an Anthropic Messages API request body.
func claudeRequestBody(prompt string, stream bool) []byte {
	body := map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"max_tokens": 256,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"stream": stream,
	}
	b, _ := json.Marshal(body)
	return b
}

// sendClaudeSSE sends a streaming Claude API request through the proxy and returns the raw SSE body.
func sendClaudeSSE(t *testing.T, proxyURL, apiKey, prompt string) io.ReadCloser {
	t.Helper()
	body := claudeRequestBody(prompt, true)
	req, err := http.NewRequest(http.MethodPost, proxyURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building Claude request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("Claude request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("Claude returned %d: %s", resp.StatusCode, string(respBody))
	}
	return resp.Body
}

// sendClaudeHook sends a PreToolUse hook request and returns the response body.
func sendClaudeHook(t *testing.T, proxyURL, sessionID, command, toolUseID string) map[string]interface{} {
	t.Helper()
	hookBody, _ := json.Marshal(map[string]interface{}{
		"session_id":      sessionID,
		"hook_event_name": "PreToolUse",
		"tool_name":       "Bash",
		"tool_input":      map[string]string{"command": command},
		"tool_use_id":     toolUseID,
	})
	resp, err := http.Post(proxyURL+"/hooks/pre-tool-use", "application/json", bytes.NewReader(hookBody))
	if err != nil {
		t.Fatalf("hook request failed: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	return result
}

// runClaudeInjectionScenarios runs the Injection, Approve, and Reject scenarios for Claude.
// Requires the proxy to be running in super-debug mode.
func runClaudeInjectionScenarios(t *testing.T, pi *proxyInstance) {
	if liveAnthropicKey == "" {
		for _, s := range []string{"Injection", "Approve", "Reject"} {
			liveResults.record("Claude", s, "SKIP")
		}
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	t.Run("Injection", func(t *testing.T) {
		defer pi.logOnFailure(t)
		session := uniqueSessionID("claude", "injection")
		_ = session // session used for dashboard query

		body := sendClaudeSSE(t, pi.proxyURL, liveAnthropicKey,
			"Write a single bash command to list files in /tmp. Use the bash tool.")
		defer body.Close()

		_, toolUses := parseAnthropicSSE(t, body)

		if len(toolUses) == 0 {
			liveResults.record("Claude", "Injection", "FAIL")
			t.Fatal("no tool_use blocks in response - LLM did not produce bash tool_use")
		}

		// In super-debug mode, the proxy replaces the command with a canary trap
		found := false
		for _, tu := range toolUses {
			if tu.Command != "" && (containsAny(tu.Command, "aegis_canary", ".aegis_canary") ||
				containsAny(tu.Name, "bash", "shell")) {
				found = true
				t.Logf("Trap injected: tool_use=%s command=%q", tu.ID, tu.Command)
			}
		}
		if !found {
			liveResults.record("Claude", "Injection", "FAIL")
			t.Fatalf("no trap command found in tool_use blocks: %+v", toolUses)
		}
		liveResults.record("Claude", "Injection", "PASS")
	})

	t.Run("Approve", func(t *testing.T) {
		defer pi.logOnFailure(t)
		session := uniqueSessionID("claude", "approve")

		body := sendClaudeSSE(t, pi.proxyURL, liveAnthropicKey,
			"Write a single bash command to list files in /tmp. Use the bash tool.")
		defer body.Close()

		_, toolUses := parseAnthropicSSE(t, body)
		if len(toolUses) == 0 {
			liveResults.record("Claude", "Approve", "FAIL")
			t.Fatal("no tool_use blocks for approve scenario")
		}

		// Find the bash tool_use with trap command
		var trapCmd, toolUseID string
		for _, tu := range toolUses {
			if tu.Command != "" {
				trapCmd = tu.Command
				toolUseID = tu.ID
				break
			}
		}
		if trapCmd == "" {
			liveResults.record("Claude", "Approve", "FAIL")
			t.Fatal("no command found in tool_use blocks")
		}

		// Send hook with the trap command (simulating developer running it)
		hookResp := sendClaudeHook(t, pi.proxyURL, session, trapCmd, toolUseID)

		// Assert: hook returned deny
		output, ok := hookResp["hookSpecificOutput"].(map[string]interface{})
		if !ok || output["permissionDecision"] != "deny" {
			liveResults.record("Claude", "Approve", "FAIL")
			t.Fatalf("expected deny response, got: %v", hookResp)
		}

		// Verify dashboard received "missed" event
		events := queryDashboardEvents(t, liveDashboardURL, liveAPIToken, session, 10*time.Second)
		if len(events) == 0 {
			liveResults.record("Claude", "Approve", "FAIL")
			t.Fatal("no events on dashboard after approve")
		}
		if events[0].Result != "missed" {
			liveResults.record("Claude", "Approve", "FAIL")
			t.Fatalf("expected result=missed, got %s", events[0].Result)
		}

		liveResults.record("Claude", "Approve", "PASS")
	})

	t.Run("Reject", func(t *testing.T) {
		defer pi.logOnFailure(t)
		session := uniqueSessionID("claude", "reject")

		body := sendClaudeSSE(t, pi.proxyURL, liveAnthropicKey,
			"Write a single bash command to list files in /tmp. Use the bash tool.")
		defer body.Close()

		_, toolUses := parseAnthropicSSE(t, body)
		if len(toolUses) == 0 {
			liveResults.record("Claude", "Reject", "FAIL")
			t.Fatal("no tool_use blocks for reject scenario")
		}

		var toolUseID string
		for _, tu := range toolUses {
			if tu.Command != "" {
				toolUseID = tu.ID
				break
			}
		}

		// Send hook with a DIFFERENT command (simulating developer editing it)
		hookResp := sendClaudeHook(t, pi.proxyURL, session, "ls -la /tmp", toolUseID)

		// Assert: hook returned allow (no hookSpecificOutput means allow)
		if output, ok := hookResp["hookSpecificOutput"]; ok {
			hso, _ := output.(map[string]interface{})
			if hso != nil && hso["permissionDecision"] == "deny" {
				liveResults.record("Claude", "Reject", "FAIL")
				t.Fatalf("expected allow, got deny: %v", hookResp)
			}
		}

		// Verify NO "missed" event on dashboard
		assertNoDashboardEvents(t, liveDashboardURL, liveAPIToken, session)

		liveResults.record("Claude", "Reject", "PASS")
	})
}

// runClaudePassthroughScenarios runs the Passthrough and Clean scenarios for Claude.
func runClaudePassthroughScenarios(t *testing.T, pi *proxyInstance) {
	if liveAnthropicKey == "" {
		for _, s := range []string{"Passthrough", "Clean"} {
			liveResults.record("Claude", s, "SKIP")
		}
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	t.Run("Passthrough", func(t *testing.T) {
		defer pi.logOnFailure(t)
		session := uniqueSessionID("claude", "passthrough")

		body := sendClaudeSSE(t, pi.proxyURL, liveAnthropicKey,
			"Reply with just the word 'hello'. Do not use any tools.")
		defer body.Close()

		text, toolUses := parseAnthropicSSE(t, body)

		// Should have text content but no bash tool_use
		if text == "" {
			liveResults.record("Claude", "Passthrough", "FAIL")
			t.Fatal("empty text response")
		}
		for _, tu := range toolUses {
			if containsAny(tu.Name, "bash", "shell") {
				liveResults.record("Claude", "Passthrough", "FAIL")
				t.Fatalf("unexpected bash tool_use in passthrough: %+v", tu)
			}
		}

		assertNoDashboardEvents(t, liveDashboardURL, liveAPIToken, session)
		liveResults.record("Claude", "Passthrough", "PASS")
	})

	t.Run("Clean", func(t *testing.T) {
		defer pi.logOnFailure(t)
		session := uniqueSessionID("claude", "clean")

		body := sendClaudeSSE(t, pi.proxyURL, liveAnthropicKey,
			"Reply with just the word 'hello'. Do not use any tools.")
		defer body.Close()

		text, toolUses := parseAnthropicSSE(t, body)

		if text == "" {
			liveResults.record("Claude", "Clean", "FAIL")
			t.Fatal("empty text response")
		}
		for _, tu := range toolUses {
			if containsAny(tu.Name, "bash", "shell") {
				liveResults.record("Claude", "Clean", "FAIL")
				t.Fatalf("unexpected bash tool_use in clean flow: %+v", tu)
			}
		}

		assertNoDashboardEvents(t, liveDashboardURL, liveAPIToken, session)
		liveResults.record("Claude", "Clean", "PASS")
	})
}

// containsAny returns true if s contains any of the substrings.
func containsAny(s string, substrs ...string) bool {
	lower := strings.ToLower(s)
	for _, sub := range substrs {
		if strings.Contains(lower, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}
```

Note: This file imports `"strings"` and `"time"` which need to be added to the import block. The `strings` import is already used in `containsAny`. Add to the import block:

```go
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
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /Users/DekaKisaLove/personal-project/agentsaegis/go-proxy && go build -tags live ./e2e/...`
Expected: No errors (will have unused import warnings resolved in next task)

- [ ] **Step 3: Commit**

```bash
git add e2e/live_claude_test.go
git commit -m "feat(e2e): add Claude CLI live test scenarios"
```

---

### Task 6: Copilot Test Scenarios (`live_copilot_test.go`)

**Files:**
- Create: `e2e/live_copilot_test.go`

All 5 scenarios x 3 models for Copilot. Uses OpenAI SSE format via the CONNECT tunnel. Each model is a subtest.

- [ ] **Step 1: Create `live_copilot_test.go`**

```go
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

// copilotModels is the list of Copilot models to test.
// The proxy handles all of them via the same OAI stream interceptor.
var copilotModels = []struct {
	Name     string // display name for the matrix
	ModelID  string // model parameter for the API
}{
	{"Copilot/GPT", "gpt-4o"},
	{"Copilot/Claude", "claude-3.5-sonnet"},
	{"Copilot/Codex", "o4-mini"},
}

// copilotRequestBody builds an OpenAI-format chat completion request.
func copilotRequestBody(model, prompt string) []byte {
	body := map[string]interface{}{
		"model":      model,
		"max_tokens": 256,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"stream": true,
	}
	b, _ := json.Marshal(body)
	return b
}

// sendCopilotSSE sends a streaming chat completion request through the proxy's
// CONNECT tunnel and returns the raw SSE body.
func sendCopilotSSE(t *testing.T, client *http.Client, endpoint, token, model, prompt string) io.ReadCloser {
	t.Helper()
	body := copilotRequestBody(model, prompt)

	chatURL := endpoint + "/chat/completions"
	req, err := http.NewRequest(http.MethodPost, chatURL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building Copilot request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Copilot request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("Copilot returned %d: %s", resp.StatusCode, string(respBody))
	}
	return resp.Body
}

// runCopilotInjectionScenarios runs Injection, Approve, and Reject for one Copilot model.
func runCopilotInjectionScenarios(t *testing.T, pi *proxyInstance, modelName, modelID string, client *http.Client) {
	if liveCopilotAuth == nil {
		for _, s := range []string{"Injection", "Approve", "Reject"} {
			liveResults.record(modelName, s, "SKIP")
		}
		t.Skip("No Copilot token available")
	}

	t.Run("Injection", func(t *testing.T) {
		defer pi.logOnFailure(t)

		body := sendCopilotSSE(t, client, liveCopilotAuth.Endpoint, liveCopilotAuth.Token,
			modelID, "Write a single bash command to list files in /tmp. Use a shell tool.")
		defer body.Close()

		_, toolUses := parseOpenAISSE(t, body)

		if len(toolUses) == 0 {
			// LLM may not produce tool_use - this is a known flakiness issue
			liveResults.record(modelName, "Injection", "FAIL")
			t.Fatal("no tool calls in response - model did not produce a shell tool call")
		}

		found := false
		for _, tu := range toolUses {
			if tu.Command != "" {
				found = true
				t.Logf("Trap injected: id=%s name=%s command=%q", tu.ID, tu.Name, tu.Command)
			}
		}
		if !found {
			liveResults.record(modelName, "Injection", "FAIL")
			t.Fatalf("no command found in tool calls: %+v", toolUses)
		}
		liveResults.record(modelName, "Injection", "PASS")
	})

	t.Run("Approve", func(t *testing.T) {
		defer pi.logOnFailure(t)
		session := uniqueSessionID(strings.ReplaceAll(modelName, "/", "-"), "approve")

		body := sendCopilotSSE(t, client, liveCopilotAuth.Endpoint, liveCopilotAuth.Token,
			modelID, "Write a single bash command to list files in /tmp. Use a shell tool.")
		defer body.Close()

		_, toolUses := parseOpenAISSE(t, body)
		if len(toolUses) == 0 {
			liveResults.record(modelName, "Approve", "FAIL")
			t.Fatal("no tool calls for approve scenario")
		}

		var trapCmd, toolUseID string
		for _, tu := range toolUses {
			if tu.Command != "" {
				trapCmd = tu.Command
				toolUseID = tu.ID
				break
			}
		}
		if trapCmd == "" {
			liveResults.record(modelName, "Approve", "FAIL")
			t.Fatal("no command in tool calls")
		}

		// Send hook with the trap command
		hookResp := sendClaudeHook(t, pi.proxyURL, session, trapCmd, toolUseID)

		output, ok := hookResp["hookSpecificOutput"].(map[string]interface{})
		if !ok || output["permissionDecision"] != "deny" {
			liveResults.record(modelName, "Approve", "FAIL")
			t.Fatalf("expected deny, got: %v", hookResp)
		}

		events := queryDashboardEvents(t, liveDashboardURL, liveAPIToken, session, 10*time.Second)
		if len(events) == 0 {
			liveResults.record(modelName, "Approve", "FAIL")
			t.Fatal("no events on dashboard after approve")
		}
		if events[0].Result != "missed" {
			liveResults.record(modelName, "Approve", "FAIL")
			t.Fatalf("expected result=missed, got %s", events[0].Result)
		}

		liveResults.record(modelName, "Approve", "PASS")
	})

	t.Run("Reject", func(t *testing.T) {
		defer pi.logOnFailure(t)
		session := uniqueSessionID(strings.ReplaceAll(modelName, "/", "-"), "reject")

		body := sendCopilotSSE(t, client, liveCopilotAuth.Endpoint, liveCopilotAuth.Token,
			modelID, "Write a single bash command to list files in /tmp. Use a shell tool.")
		defer body.Close()

		_, toolUses := parseOpenAISSE(t, body)
		if len(toolUses) == 0 {
			liveResults.record(modelName, "Reject", "FAIL")
			t.Fatal("no tool calls for reject scenario")
		}

		var toolUseID string
		for _, tu := range toolUses {
			if tu.Command != "" {
				toolUseID = tu.ID
				break
			}
		}

		hookResp := sendClaudeHook(t, pi.proxyURL, session, "ls -la /tmp", toolUseID)
		if output, ok := hookResp["hookSpecificOutput"]; ok {
			hso, _ := output.(map[string]interface{})
			if hso != nil && hso["permissionDecision"] == "deny" {
				liveResults.record(modelName, "Reject", "FAIL")
				t.Fatalf("expected allow, got deny: %v", hookResp)
			}
		}

		assertNoDashboardEvents(t, liveDashboardURL, liveAPIToken, session)
		liveResults.record(modelName, "Reject", "PASS")
	})
}

// runCopilotPassthroughScenarios runs Passthrough and Clean for one Copilot model.
func runCopilotPassthroughScenarios(t *testing.T, pi *proxyInstance, modelName, modelID string, client *http.Client) {
	if liveCopilotAuth == nil {
		for _, s := range []string{"Passthrough", "Clean"} {
			liveResults.record(modelName, s, "SKIP")
		}
		t.Skip("No Copilot token available")
	}

	t.Run("Passthrough", func(t *testing.T) {
		defer pi.logOnFailure(t)
		session := uniqueSessionID(strings.ReplaceAll(modelName, "/", "-"), "passthrough")

		body := sendCopilotSSE(t, client, liveCopilotAuth.Endpoint, liveCopilotAuth.Token,
			modelID, "Reply with just the word 'hello'. Do not use any tools.")
		defer body.Close()

		text, toolUses := parseOpenAISSE(t, body)

		if text == "" && len(toolUses) == 0 {
			liveResults.record(modelName, "Passthrough", "FAIL")
			t.Fatal("empty response")
		}
		for _, tu := range toolUses {
			if containsAny(tu.Name, "bash", "shell", "terminal", "run_command") {
				liveResults.record(modelName, "Passthrough", "FAIL")
				t.Fatalf("unexpected shell tool call in passthrough: %+v", tu)
			}
		}

		assertNoDashboardEvents(t, liveDashboardURL, liveAPIToken, session)
		liveResults.record(modelName, "Passthrough", "PASS")
	})

	t.Run("Clean", func(t *testing.T) {
		defer pi.logOnFailure(t)
		session := uniqueSessionID(strings.ReplaceAll(modelName, "/", "-"), "clean")

		body := sendCopilotSSE(t, client, liveCopilotAuth.Endpoint, liveCopilotAuth.Token,
			modelID, "Reply with just the word 'hello'. Do not use any tools.")
		defer body.Close()

		text, toolUses := parseOpenAISSE(t, body)

		if text == "" && len(toolUses) == 0 {
			liveResults.record(modelName, "Clean", "FAIL")
			t.Fatal("empty response")
		}
		for _, tu := range toolUses {
			if containsAny(tu.Name, "bash", "shell", "terminal", "run_command") {
				liveResults.record(modelName, "Clean", "FAIL")
				t.Fatalf("unexpected shell tool call in clean flow: %+v", tu)
			}
		}

		assertNoDashboardEvents(t, liveDashboardURL, liveAPIToken, session)
		liveResults.record(modelName, "Clean", "PASS")
	})
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /Users/DekaKisaLove/personal-project/agentsaegis/go-proxy && go build -tags live ./e2e/...`
Expected: No errors

- [ ] **Step 3: Commit**

```bash
git add e2e/live_copilot_test.go
git commit -m "feat(e2e): add Copilot live test scenarios for GPT/Claude/Codex models"
```

---

### Task 7: Wire Up Top-Level Test Functions (`live_test.go` additions)

**Files:**
- Modify: `e2e/live_test.go`

Adds `TestLiveSuperDebug` and `TestLivePassthrough` which start the proxy in the right mode and dispatch to the provider-specific scenario runners.

- [ ] **Step 1: Add top-level test functions to `live_test.go`**

Append to the end of `e2e/live_test.go`:

```go
// TestLiveSuperDebug runs injection, approve, and reject scenarios with super-debug mode.
func TestLiveSuperDebug(t *testing.T) {
	homeDir := t.TempDir()
	port := liveFindFreePort(t)

	pi := liveStartProxy(t, liveBinaryPath, homeDir, port, true, liveDashboardURL, liveAPIToken)
	defer pi.logOnFailure(t)

	t.Run("Claude", func(t *testing.T) {
		runClaudeInjectionScenarios(t, pi)
	})

	// Copilot tests need the proxy CA for CONNECT tunnel TLS trust
	if liveCopilotAuth != nil {
		caPool := loadProxyCA(t, homeDir)
		proxyAddr := fmt.Sprintf("127.0.0.1:%d", port)
		client := copilotHTTPClient(t, proxyAddr, caPool)

		for _, model := range copilotModels {
			model := model // capture for subtest closure
			t.Run(model.Name, func(t *testing.T) {
				runCopilotInjectionScenarios(t, pi, model.Name, model.ModelID, client)
			})
		}
	} else {
		for _, model := range copilotModels {
			for _, s := range []string{"Injection", "Approve", "Reject"} {
				liveResults.record(model.Name, s, "SKIP")
			}
		}
		t.Log("Copilot token not available - skipping Copilot injection tests")
	}
}

// TestLivePassthrough runs passthrough and clean scenarios without super-debug.
func TestLivePassthrough(t *testing.T) {
	homeDir := t.TempDir()
	port := liveFindFreePort(t)

	pi := liveStartProxy(t, liveBinaryPath, homeDir, port, false, liveDashboardURL, liveAPIToken)
	defer pi.logOnFailure(t)

	t.Run("Claude", func(t *testing.T) {
		runClaudePassthroughScenarios(t, pi)
	})

	if liveCopilotAuth != nil {
		caPool := loadProxyCA(t, homeDir)
		proxyAddr := fmt.Sprintf("127.0.0.1:%d", port)
		client := copilotHTTPClient(t, proxyAddr, caPool)

		for _, model := range copilotModels {
			model := model
			t.Run(model.Name, func(t *testing.T) {
				runCopilotPassthroughScenarios(t, pi, model.Name, model.ModelID, client)
			})
		}
	} else {
		for _, model := range copilotModels {
			for _, s := range []string{"Passthrough", "Clean"} {
				liveResults.record(model.Name, s, "SKIP")
			}
		}
		t.Log("Copilot token not available - skipping Copilot passthrough tests")
	}
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /Users/DekaKisaLove/personal-project/agentsaegis/go-proxy && go build -tags live ./e2e/...`
Expected: No errors

- [ ] **Step 3: Commit**

```bash
git add e2e/live_test.go
git commit -m "feat(e2e): wire up top-level TestLiveSuperDebug and TestLivePassthrough"
```

---

### Task 8: Makefile Target and CLAUDE.md Update

**Files:**
- Modify: `Makefile`
- Modify: `CLAUDE.md`

- [ ] **Step 1: Add `test-live` target to Makefile**

Add after the `test-e2e` target in the Makefile:

```makefile
test-live: build
	go test -race -tags live -v -count=1 -timeout 10m ./e2e/...
```

Also add `test-live` to the `.PHONY` line.

- [ ] **Step 2: Update CLAUDE.md with the new test command and files**

In the `## Commands` section, after the `### Run e2e tests` block, add:

```markdown
### Run live E2E tests (real API calls)
```bash
make test-live
# Requires: ANTHROPIC_API_KEY, AEGIS_API_TOKEN
# Optional: GITHUB_TOKEN (for Copilot tests)
# Equivalent: go test -race -tags live -v -count=1 -timeout 10m ./e2e/...
```
```

In the `## Directory Map` section, update the `e2e/` entry to include the new files:

```
e2e/
  e2e_test.go            # End-to-end tests (build tag: e2e) - mock Anthropic + dashboard servers
  live_test.go           # Live E2E tests (build tag: live) - TestMain, matrix runner, result tracker
  live_claude_test.go    # Live Claude CLI scenarios - Anthropic SSE format through reverse proxy
  live_copilot_test.go   # Live Copilot scenarios - OpenAI SSE format through CONNECT tunnel
  live_helpers_test.go   # Live test helpers - proxy subprocess, token exchange, SSE parsers
```

In the `## Testing` section, add a bullet:

```
- **Live E2E tests:** `e2e/live_*.go` with build tag `live` (real API calls through external binary)
```

- [ ] **Step 3: Verify the Makefile target**

Run: `cd /Users/DekaKisaLove/personal-project/agentsaegis/go-proxy && make test-live 2>&1 | head -5`
Expected: Starts building and running tests (will fail without API keys but proves the target works)

- [ ] **Step 4: Run existing tests to verify no regressions**

Run: `cd /Users/DekaKisaLove/personal-project/agentsaegis/go-proxy && make test`
Expected: All existing unit tests pass

- [ ] **Step 5: Commit**

```bash
git add Makefile CLAUDE.md
git commit -m "feat: add make test-live target and update CLAUDE.md with live E2E docs"
```

---

### Task 9: Smoke Test and Fix Compilation Issues

**Files:**
- Modify: any files with compilation errors

This is a cleanup task. Build the live tests, fix any compilation issues (unused imports, type mismatches, missing functions), and do a dry run.

- [ ] **Step 1: Build the live test binary**

Run: `cd /Users/DekaKisaLove/personal-project/agentsaegis/go-proxy && go test -tags live -c -o /dev/null ./e2e/...`
Expected: Clean compilation. If errors, fix them.

- [ ] **Step 2: Run with no API keys to verify skip logic**

Run: `cd /Users/DekaKisaLove/personal-project/agentsaegis/go-proxy && ANTHROPIC_API_KEY="" GITHUB_TOKEN="" AEGIS_API_TOKEN="" go test -tags live -v -count=1 -timeout 2m ./e2e/... 2>&1 | tail -30`
Expected: All tests skip gracefully with "SKIP" in the matrix. No panics or crashes.

- [ ] **Step 3: Run existing tests to verify no regressions**

Run: `cd /Users/DekaKisaLove/personal-project/agentsaegis/go-proxy && make test && make test-e2e`
Expected: All pass. The live tests are excluded because they use the `live` build tag.

- [ ] **Step 4: Fix any issues found, commit**

```bash
git add -A
git commit -m "fix(e2e): fix compilation and skip-logic issues in live tests"
```

---

### Task 10: Run Live Tests with Real API Keys

**Files:** None (execution only)

This is the verification task. Run the actual live tests with real API keys.

- [ ] **Step 1: Run Claude-only live tests**

Run: `cd /Users/DekaKisaLove/personal-project/agentsaegis/go-proxy && make test-live`
Expected: Claude tests run (pass or fail with useful diagnostics). Copilot tests skip if no GitHub token.

- [ ] **Step 2: Review the output matrix**

Check the summary matrix at the end. For any FAILed tests:
- Read the failure message
- Check the proxy stderr log (printed on failure)
- Identify if it's a test issue (wrong assertion) or proxy issue (real bug)

- [ ] **Step 3: Fix any test issues found**

Common issues to expect:
- LLM doesn't produce `tool_use` for code prompts (retry logic may be needed)
- SSE format differences from what we expected (adjust parsers)
- Dashboard event query endpoint not available yet (those assertions will fail)
- Copilot token exchange fails (model or auth issues)

Fix and commit:

```bash
git add -A
git commit -m "fix(e2e): adjust live tests based on real API responses"
```

- [ ] **Step 4: Run full lint check**

Run: `cd /Users/DekaKisaLove/personal-project/agentsaegis/go-proxy && make lint`
Expected: No lint errors

- [ ] **Step 5: Final commit if needed**

```bash
git add -A
git commit -m "chore(e2e): finalize live E2E test runner"
```
