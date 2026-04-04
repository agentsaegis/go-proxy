//go:build live

package e2e_test

import (
	"bufio"
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
	"syscall"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Part 1: Proxy subprocess lifecycle
// ---------------------------------------------------------------------------

// liveFindFreePort finds a free TCP port on localhost.
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

// liveWaitForHealth polls GET /__aegis/health until it returns 200 or the
// timeout expires.
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
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("proxy at %s not healthy within %s", proxyURL, timeout)
}

// liveWriteConfig writes a minimal ~/.agentsaegis/config.yaml into the given
// home directory so the binary can pick up dashboard URL and API token.
func liveWriteConfig(t *testing.T, homeDir, dashboardURL, apiToken string) {
	t.Helper()
	configDir := filepath.Join(homeDir, ".agentsaegis")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("liveWriteConfig: mkdir: %v", err)
	}
	yaml := fmt.Sprintf("dashboard_url: %q\napi_token: %q\n", dashboardURL, apiToken)
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("liveWriteConfig: write: %v", err)
	}
}

// syncBuffer is a thread-safe bytes.Buffer for capturing subprocess output.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (sb *syncBuffer) Write(p []byte) (n int, err error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.Write(p)
}

func (sb *syncBuffer) String() string {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.String()
}

func (sb *syncBuffer) Len() int {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.Len()
}

// proxyInstance holds everything needed to manage and interact with a running
// proxy subprocess.
type proxyInstance struct {
	cmd      *exec.Cmd
	port     int
	homeDir  string
	proxyURL string
	stderr   *syncBuffer
	cancel   context.CancelFunc
}

// liveStartProxy starts the compiled proxy binary as a subprocess. It sets
// the required environment variables, waits for the health endpoint to
// respond, and registers a cleanup function that stops the proxy.
func liveStartProxy(
	t *testing.T,
	binaryPath string,
	homeDir string,
	port int,
	superDebug bool,
	dashboardURL string,
	apiToken string,
) *proxyInstance {
	t.Helper()

	liveWriteConfig(t, homeDir, dashboardURL, apiToken)

	args := []string{"start"}
	if superDebug {
		args = append(args, "--super-debug")
	} else {
		args = append(args, "--debug")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, binaryPath, args...)

	stderrBuf := &syncBuffer{}
	cmd.Stderr = stderrBuf

	cmd.Env = append(os.Environ(),
		"HOME="+homeDir,
		fmt.Sprintf("AEGIS_PROXY_PORT=%d", port),
		"AEGIS_DASHBOARD_URL="+dashboardURL,
		"AEGIS_API_TOKEN="+apiToken,
	)

	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("liveStartProxy: start binary: %v", err)
	}

	pi := &proxyInstance{
		cmd:      cmd,
		port:     port,
		homeDir:  homeDir,
		proxyURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		stderr:   stderrBuf,
		cancel:   cancel,
	}

	// Wait for the proxy to become healthy. 15s because the binary
	// generates a CA certificate on first start.
	liveWaitForHealth(t, pi.proxyURL, 15*time.Second)

	t.Cleanup(func() {
		pi.stop()
		pi.logOnFailure(t)
	})

	return pi
}

// stop sends SIGTERM to the proxy process and waits up to 5 seconds for a
// clean exit. If the process is still alive after 5 seconds, the context
// cancel function force-kills it.
func (pi *proxyInstance) stop() {
	if pi.cmd.Process == nil {
		return
	}
	// Send SIGTERM for graceful shutdown.
	_ = pi.cmd.Process.Signal(syscall.SIGTERM)

	done := make(chan struct{})
	go func() {
		_ = pi.cmd.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Clean exit.
	case <-time.After(5 * time.Second):
		// Force kill via context cancel.
		pi.cancel()
		<-done
	}
}

// logOnFailure dumps the proxy's stderr output when the test has failed.
func (pi *proxyInstance) logOnFailure(t *testing.T) {
	t.Helper()
	if t.Failed() && pi.stderr.Len() > 0 {
		t.Logf("--- proxy stderr ---\n%s", pi.stderr.String())
	}
}

// loadProxyCA reads the proxy's generated CA certificate from
// $HOME/.agentsaegis/ca.pem and returns a cert pool that trusts it.
func loadProxyCA(t *testing.T, homeDir string) *x509.CertPool {
	t.Helper()
	caPath := filepath.Join(homeDir, ".agentsaegis", "ca.pem")
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatalf("loadProxyCA: read %s: %v", caPath, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatalf("loadProxyCA: failed to parse CA certificate from %s", caPath)
	}
	return pool
}

// ---------------------------------------------------------------------------
// Part 2: Copilot auth
// ---------------------------------------------------------------------------

// copilotAuth holds the token and API endpoint obtained from the Copilot
// token exchange.
type copilotAuth struct {
	Token    string
	Endpoint string
}

// readCopilotAppTokens reads Copilot OAuth tokens from the local Copilot
// CLI config at ~/.config/github-copilot/apps.json. These ghu_ tokens are
// bound to Copilot-specific GitHub App IDs and can be exchanged for
// short-lived Copilot API tokens.
func readCopilotAppTokens() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(home, ".config", "github-copilot", "apps.json"))
	if err != nil {
		return nil
	}
	var apps map[string]struct {
		OAuthToken string `json:"oauth_token"`
	}
	if err := json.Unmarshal(data, &apps); err != nil {
		return nil
	}
	var tokens []string
	for _, app := range apps {
		if app.OAuthToken != "" {
			tokens = append(tokens, app.OAuthToken)
		}
	}
	return tokens
}

// exchangeCopilotToken exchanges a GitHub token (OAuth or Copilot app token)
// for a short-lived Copilot API token via the internal token API. Returns nil
// if the exchange fails (wrong token type, no Copilot access, etc.).
func exchangeCopilotToken(ghToken string) *copilotAuth {
	req, err := http.NewRequest(http.MethodGet,
		"https://api.github.com/copilot_internal/v2/token", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "token "+ghToken)
	req.Header.Set("Accept", "application/json")
	// The Copilot token API rejects requests from unrecognised user-agents
	// (Go's default Go-http-client/2.0 gets a 403). Identify as a VS Code
	// integration to pass the client allow-list.
	req.Header.Set("Editor-Version", "vscode/1.99.0")
	req.Header.Set("Editor-Plugin-Version", "copilot/1.0.0")
	req.Header.Set("User-Agent", "GithubCopilot/1.0.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var result struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}
	if result.Token == "" {
		return nil
	}

	endpoint := result.Endpoints.API
	if endpoint == "" {
		endpoint = "https://api.individual.githubcopilot.com"
	}
	return &copilotAuth{Token: result.Token, Endpoint: endpoint}
}

// acquireCopilotToken obtains a Copilot API token by trying multiple sources:
// 1. GITHUB_TOKEN env var (for CI)
// 2. Copilot OAuth tokens from ~/.config/github-copilot/apps.json (local dev)
// 3. gh CLI auth token (fallback)
// Returns nil if no valid token is available (caller should t.Skip).
func acquireCopilotToken(t *testing.T) *copilotAuth {
	t.Helper()

	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		if auth := exchangeCopilotToken(tok); auth != nil {
			return auth
		}
	}

	for _, tok := range readCopilotAppTokens() {
		if auth := exchangeCopilotToken(tok); auth != nil {
			return auth
		}
	}

	out, err := exec.Command("gh", "auth", "token").Output()
	if err == nil {
		tok := strings.TrimSpace(string(out))
		if tok != "" {
			if auth := exchangeCopilotToken(tok); auth != nil {
				return auth
			}
		}
	}

	return nil
}

// copilotHTTPClient returns an HTTP client configured to route requests
// through the proxy's CONNECT tunnel with TLS trust for the proxy CA.
func copilotHTTPClient(t *testing.T, proxyAddr string, caPool *x509.CertPool) *http.Client {
	t.Helper()

	proxyURL, err := url.Parse("http://" + proxyAddr)
	if err != nil {
		t.Fatalf("copilotHTTPClient: parse proxy addr: %v", err)
	}

	return &http.Client{
		Timeout: 120 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				RootCAs:    caPool,
				MinVersion: tls.VersionTLS13,
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Part 3: Dashboard query and SSE parsers
// ---------------------------------------------------------------------------

// dashboardEvent represents a trap event as returned by the dashboard API.
type dashboardEvent struct {
	TrapTemplateID string `json:"trap_template_id"`
	TrapCategory   string `json:"trap_category"`
	Result         string `json:"result"`
	SessionID      string `json:"session_id"`
}

// queryDashboardEvents polls the dashboard API for events matching the
// given session ID. It retries every 200ms until at least one event is
// found or the timeout expires.
func queryDashboardEvents(
	t *testing.T,
	dashboardURL string,
	apiToken string,
	sessionID string,
	timeout time.Duration,
) []dashboardEvent {
	t.Helper()

	endpoint := dashboardURL + "/api/proxy/events?session_id=" + url.QueryEscape(sessionID)
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			t.Fatalf("queryDashboardEvents: build request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+apiToken)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			time.Sleep(200 * time.Millisecond)
			continue
		}

		var envelope struct {
			Events []dashboardEvent `json:"events"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if len(envelope.Events) > 0 {
			return envelope.Events
		}
		time.Sleep(200 * time.Millisecond)
	}

	return nil
}

// assertNoDashboardEvents waits 3 seconds then asserts that no events were
// reported for the given session ID.
func assertNoDashboardEvents(t *testing.T, dashboardURL, apiToken, sessionID string) {
	t.Helper()

	time.Sleep(3 * time.Second)

	endpoint := dashboardURL + "/api/proxy/events?session_id=" + url.QueryEscape(sessionID)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatalf("assertNoDashboardEvents: build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Dashboard unreachable - no events is the expected outcome.
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return
	}

	var envelope struct {
		Events []dashboardEvent `json:"events"`
	}
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &envelope); err != nil {
		return
	}
	if len(envelope.Events) > 0 {
		t.Fatalf("assertNoDashboardEvents: expected 0 events for session %q, got %d: %+v",
			sessionID, len(envelope.Events), envelope.Events)
	}
}

// sseToolUse represents a tool_use block extracted from an SSE stream.
type sseToolUse struct {
	ID       string
	Name     string
	Command  string
	RawInput string
}

// ---------------------------------------------------------------------------
// lineScanner - large-buffer SSE line reader
// ---------------------------------------------------------------------------

// lineScanner reads lines from a reader with a large buffer. The default
// bufio.Scanner has a 64KB max token size which is too small for SSE lines
// from AI APIs.
type lineScanner struct {
	reader  *bufio.Reader
	current string
	err     error
}

// newLineScanner creates a lineScanner with a 4MB buffer.
func newLineScanner(r io.Reader) *lineScanner {
	return &lineScanner{
		reader: bufio.NewReaderSize(r, 4*1024*1024),
	}
}

// Scan reads the next line. Returns true if a line was read, false on EOF
// or error.
func (ls *lineScanner) Scan() bool {
	var buf []byte
	for {
		chunk, isPrefix, err := ls.reader.ReadLine()
		buf = append(buf, chunk...)
		if err != nil {
			ls.err = err
			if len(buf) > 0 {
				ls.current = string(buf)
				return true
			}
			return false
		}
		if !isPrefix {
			ls.current = string(buf)
			return true
		}
	}
}

// Text returns the last line read by Scan.
func (ls *lineScanner) Text() string {
	return ls.current
}

// Err returns the first non-EOF error encountered.
func (ls *lineScanner) Err() error {
	if ls.err == io.EOF {
		return nil
	}
	return ls.err
}

// ---------------------------------------------------------------------------
// Anthropic SSE parser
// ---------------------------------------------------------------------------

// parseAnthropicSSE parses an Anthropic-format SSE response body and
// extracts accumulated text and tool_use blocks.
//
// Event types handled:
//   - content_block_start: detect tool_use blocks (capture id, name)
//   - content_block_delta: accumulate text_delta and input_json_delta
//   - content_block_stop: flush completed tool_use with parsed command
func parseAnthropicSSE(t *testing.T, body []byte) (text string, toolUses []sseToolUse) {
	t.Helper()

	type blockState struct {
		index    int
		id       string
		name     string
		isToolUse bool
		jsonBuf  strings.Builder
	}

	var textBuf strings.Builder
	blocks := make(map[int]*blockState)

	scanner := newLineScanner(bytes.NewReader(body))
	var currentEvent string

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		switch currentEvent {
		case "content_block_start":
			var ev struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				continue
			}
			bs := &blockState{
				index:    ev.Index,
				id:       ev.ContentBlock.ID,
				name:     ev.ContentBlock.Name,
				isToolUse: ev.ContentBlock.Type == "tool_use",
			}
			blocks[ev.Index] = bs

		case "content_block_delta":
			var ev struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				textBuf.WriteString(ev.Delta.Text)
			case "input_json_delta":
				if bs, ok := blocks[ev.Index]; ok && bs.isToolUse {
					bs.jsonBuf.WriteString(ev.Delta.PartialJSON)
				}
			}

		case "content_block_stop":
			var ev struct {
				Index int `json:"index"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				continue
			}
			bs, ok := blocks[ev.Index]
			if !ok || !bs.isToolUse {
				continue
			}
			raw := bs.jsonBuf.String()
			tu := sseToolUse{
				ID:       bs.id,
				Name:     bs.name,
				RawInput: raw,
			}
			// Parse {"command":"..."} from accumulated input JSON.
			var input struct {
				Command string `json:"command"`
			}
			if err := json.Unmarshal([]byte(raw), &input); err == nil {
				tu.Command = input.Command
			}
			toolUses = append(toolUses, tu)
		}
	}

	text = textBuf.String()
	return text, toolUses
}

// ---------------------------------------------------------------------------
// OpenAI SSE parser
// ---------------------------------------------------------------------------

// parseOpenAISSE parses an OpenAI-format SSE response body (used by
// Copilot) and extracts accumulated text and tool_call blocks.
//
// Event structure:
//   - choices[].delta.content for text
//   - choices[].delta.tool_calls[] with index, id, function.name, function.arguments
//   - finish_reason="tool_calls" or "stop" signals end of stream
func parseOpenAISSE(t *testing.T, body []byte) (text string, toolUses []sseToolUse) {
	t.Helper()

	type toolCallState struct {
		id      string
		name    string
		argsBuf strings.Builder
	}

	var textBuf strings.Builder
	calls := make(map[int]*toolCallState)

	scanner := newLineScanner(bytes.NewReader(body))

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
				state, ok := calls[tc.Index]
				if !ok {
					state = &toolCallState{}
					calls[tc.Index] = state
				}
				if tc.ID != "" {
					state.id = tc.ID
				}
				if tc.Function.Name != "" {
					state.name = tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					state.argsBuf.WriteString(tc.Function.Arguments)
				}
			}
		}
	}

	// Flush all accumulated tool calls.
	for i := 0; i < len(calls); i++ {
		state, ok := calls[i]
		if !ok {
			continue
		}
		raw := state.argsBuf.String()
		tu := sseToolUse{
			ID:       state.id,
			Name:     state.name,
			RawInput: raw,
		}
		var input struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(raw), &input); err == nil {
			tu.Command = input.Command
		}
		toolUses = append(toolUses, tu)
	}

	text = textBuf.String()
	return text, toolUses
}

// ---------------------------------------------------------------------------
// Part 4: Claude CLI helpers
// ---------------------------------------------------------------------------

// claudeCLIResult holds the output from a `claude -p` invocation.
type claudeCLIResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
}

// runClaudeCLI runs `claude -p` as a subprocess with ANTHROPIC_BASE_URL
// pointing to the test proxy. Configures a project-level PreToolUse hook
// so the proxy's agentsaegis hook bridge is called for trap detection.
// Uses the user's subscription auth (no API key).
func runClaudeCLI(t *testing.T, proxyPort int, binaryPath, prompt string, timeout time.Duration) *claudeCLIResult {
	t.Helper()

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("runClaudeCLI: claude binary not found: %v", err)
	}

	// Create temp working directory with project-level hook config
	workDir := t.TempDir()
	claudeDir := filepath.Join(workDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatalf("runClaudeCLI: create .claude dir: %v", err)
	}

	hookCommand := binaryPath + " hook"
	settings := fmt.Sprintf(`{
  "hooks": {
    "PreToolUse": [{
      "matcher": "Bash",
      "hooks": [{
        "type": "command",
        "command": %q
      }]
    }]
  }
}`, hookCommand)
	settingsPath := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(settings), 0o600); err != nil {
		t.Fatalf("runClaudeCLI: write settings.json: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, claudePath,
		"-p",
		"--output-format", "json",
		"--dangerously-skip-permissions",
		"--no-session-persistence",
		prompt,
	)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Set ANTHROPIC_BASE_URL to route through test proxy.
	// Set AEGIS_PROXY_PORT so the hook bridge calls the right proxy.
	// Filter out existing values to avoid conflicts with the user's shell wrapper.
	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "ANTHROPIC_BASE_URL=") &&
			!strings.HasPrefix(e, "AEGIS_PROXY_PORT=") {
			filtered = append(filtered, e)
		}
	}
	filtered = append(filtered,
		fmt.Sprintf("ANTHROPIC_BASE_URL=http://127.0.0.1:%d", proxyPort),
		fmt.Sprintf("AEGIS_PROXY_PORT=%d", proxyPort),
	)
	cmd.Env = filtered

	runErr := cmd.Run()

	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	return &claudeCLIResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		Err:      runErr,
	}
}

// runClaudeCLICatchMode runs `claude -p` with a custom hook that simulates
// a user who NOTICES the trap and edits the command. The hook detects
// canary/trap markers in the command and sends a modified command to the
// proxy (same tool_use_id, different command = "caught").
func runClaudeCLICatchMode(t *testing.T, proxyPort int, prompt string, timeout time.Duration) *claudeCLIResult {
	t.Helper()

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("runClaudeCLICatchMode: claude binary not found: %v", err)
	}

	workDir := t.TempDir()
	claudeDir := filepath.Join(workDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatalf("runClaudeCLICatchMode: create .claude dir: %v", err)
	}

	// Write a Python hook script that detects traps and modifies the command
	hookScript := fmt.Sprintf(`#!/usr/bin/env python3
import sys, json, urllib.request

data = json.load(sys.stdin)
command = data.get("tool_input", {}).get("command", "")
tool_use_id = data.get("tool_use_id", "")
session_id = data.get("session_id", "")

# If command looks like a trap, send MODIFIED command to proxy (simulating user edit)
if "aegis_canary" in command or "aegis-trap" in command:
    modified = "echo user-caught-this-trap"
    payload = json.dumps({
        "session_id": session_id,
        "hook_event_name": "PreToolUse",
        "tool_name": "Bash",
        "tool_input": {"command": modified},
        "tool_use_id": tool_use_id,
    }).encode()
    req = urllib.request.Request(
        "http://127.0.0.1:%d/hooks/pre-tool-use",
        data=payload,
        headers={"Content-Type": "application/json"},
    )
    try:
        urllib.request.urlopen(req, timeout=5)
    except Exception:
        pass
    # Allow the modified command to execute (don't block)
    sys.exit(0)
else:
    # Non-trap command - allow
    sys.exit(0)
`, proxyPort)

	hookPath := filepath.Join(workDir, "catch-hook.py")
	if err := os.WriteFile(hookPath, []byte(hookScript), 0o755); err != nil {
		t.Fatalf("runClaudeCLICatchMode: write hook script: %v", err)
	}

	settings := fmt.Sprintf(`{
  "hooks": {
    "PreToolUse": [{
      "matcher": "Bash",
      "hooks": [{
        "type": "command",
        "command": "python3 %s"
      }]
    }]
  }
}`, hookPath)
	settingsPath := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(settings), 0o600); err != nil {
		t.Fatalf("runClaudeCLICatchMode: write settings.json: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, claudePath,
		"-p",
		"--output-format", "json",
		"--dangerously-skip-permissions",
		"--no-session-persistence",
		prompt,
	)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "ANTHROPIC_BASE_URL=") &&
			!strings.HasPrefix(e, "AEGIS_PROXY_PORT=") {
			filtered = append(filtered, e)
		}
	}
	filtered = append(filtered,
		fmt.Sprintf("ANTHROPIC_BASE_URL=http://127.0.0.1:%d", proxyPort),
		fmt.Sprintf("AEGIS_PROXY_PORT=%d", proxyPort),
	)
	cmd.Env = filtered

	runErr := cmd.Run()

	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	return &claudeCLIResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		Err:      runErr,
	}
}

// liveSendHookRequest sends a PreToolUse hook request to the proxy and returns
// the parsed JSON response.
func liveSendHookRequest(t *testing.T, proxyURL, sessionID, command, toolUseID string) map[string]interface{} {
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
		t.Fatalf("liveSendHookRequest: request failed: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("liveSendHookRequest: decode response: %v", err)
	}
	return result
}

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------

// uniqueSessionID generates a unique session identifier for a live test
// scenario, using the format: live-e2e-<provider>-<scenario>-<unix_nano>.
func uniqueSessionID(provider, scenario string) string {
	return fmt.Sprintf("live-e2e-%s-%s-%d", provider, scenario, time.Now().UnixNano())
}

// truncate shortens a string to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
