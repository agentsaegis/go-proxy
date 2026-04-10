//go:build live

package e2e_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Package-level vars (set in TestMain)
// ---------------------------------------------------------------------------

var (
	liveBinaryPath      string
	liveAPIToken        string
	liveDashboardURL    string
	liveAnthropicAPIKey string
	liveCopilotAuth     *copilotAuth
)

// ---------------------------------------------------------------------------
// Result matrix
// ---------------------------------------------------------------------------

var providers = []string{"Claude", "Copilot/GPT-4o-mini", "Copilot/GPT-4.1", "Copilot/GPT-3.5"}
var scenarios = []string{"Passthrough", "Injection", "Approve", "Reject", "Clean"}

type resultTracker struct {
	mu      sync.Mutex
	results map[string]map[string]string
}

var liveResults = &resultTracker{
	results: make(map[string]map[string]string),
}

func (rt *resultTracker) record(provider, scenario, result string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.results[provider] == nil {
		rt.results[provider] = make(map[string]string)
	}
	rt.results[provider][scenario] = result
}

func printResultMatrix() {
	fmt.Println()
	fmt.Println("=== Live E2E Result Matrix ===")
	fmt.Println()

	// Header row
	header := fmt.Sprintf("%-20s", "Provider")
	for _, s := range scenarios {
		header += fmt.Sprintf(" | %-12s", s)
	}
	fmt.Println(header)
	fmt.Println(strings.Repeat("-", len(header)))

	// Data rows
	tested, passed, failed := 0, 0, 0
	for _, p := range providers {
		row := fmt.Sprintf("%-20s", p)
		providerTested := false
		for _, s := range scenarios {
			val := "-"
			liveResults.mu.Lock()
			if m, ok := liveResults.results[p]; ok {
				if v, ok := m[s]; ok {
					val = v
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
			liveResults.mu.Unlock()
			row += fmt.Sprintf(" | %-12s", val)
		}
		if providerTested {
			tested++
		}
		fmt.Println(row)
	}
	fmt.Println()
	total := passed + failed
	fmt.Printf("%d/%d providers tested, %d/%d passed, %d failed\n",
		tested, len(providers), passed, total, failed)
	fmt.Println()
}

// ---------------------------------------------------------------------------
// TestMain
// ---------------------------------------------------------------------------

func TestMain(m *testing.M) {
	liveAPIToken = os.Getenv("AEGIS_API_TOKEN")
	liveAnthropicAPIKey = os.Getenv("ANTHROPIC_API_KEY")
	liveDashboardURL = os.Getenv("AEGIS_DASHBOARD_URL")
	if liveDashboardURL == "" {
		liveDashboardURL = "https://agentsaegis.com"
	}

	// Build the proxy binary
	repoRoot := findRepoRoot()
	tmpDir, err := os.MkdirTemp("", "aegis-live-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	liveBinaryPath = filepath.Join(tmpDir, "agentsaegis")
	cmd := exec.Command("go", "build", "-o", liveBinaryPath, "./cmd/agentsaegis")
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: build binary: %v\n", err)
		os.Exit(1)
	}

	// Acquire Copilot token once (non-fatal if unavailable)
	liveCopilotAuth = acquireCopilotTokenForMain()

	code := m.Run()
	printResultMatrix()
	os.Exit(code)
}

// acquireCopilotTokenForMain is the non-testing.T version of acquireCopilotToken.
// It logs to stderr and returns nil on failure instead of calling t.Fatalf.
func acquireCopilotTokenForMain() *copilotAuth {
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		if auth := exchangeCopilotToken(tok); auth != nil {
			fmt.Fprintln(os.Stderr, "Copilot token acquired via GITHUB_TOKEN")
			return auth
		}
	}

	for _, tok := range readCopilotAppTokens() {
		if auth := exchangeCopilotToken(tok); auth != nil {
			fmt.Fprintln(os.Stderr, "Copilot token acquired via ~/.config/github-copilot/apps.json")
			return auth
		}
	}

	out, err := exec.Command("gh", "auth", "token").Output()
	if err == nil {
		tok := strings.TrimSpace(string(out))
		if tok != "" {
			if auth := exchangeCopilotToken(tok); auth != nil {
				fmt.Fprintln(os.Stderr, "Copilot token acquired via gh auth token")
				return auth
			}
		}
	}

	fmt.Fprintln(os.Stderr, "acquireCopilotTokenForMain: no valid Copilot token found, skipping Copilot tests")
	return nil
}

// findRepoRoot walks up from cwd looking for go.mod.
func findRepoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "findRepoRoot: getwd: %v\n", err)
		os.Exit(1)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			fmt.Fprintln(os.Stderr, "findRepoRoot: could not find go.mod")
			os.Exit(1)
		}
		dir = parent
	}
}

// ---------------------------------------------------------------------------
// Top-level test functions
// ---------------------------------------------------------------------------

func TestLiveSuperDebug(t *testing.T) {
	if liveAPIToken == "" {
		t.Skip("AEGIS_API_TOKEN not set, skipping live tests")
	}

	homeDir := t.TempDir()
	port := liveFindFreePort(t)
	pi := liveStartProxy(t, liveBinaryPath, homeDir, port, true, liveDashboardURL, liveAPIToken)

	// Claude injection scenarios
	t.Run("Claude", func(t *testing.T) {
		runClaudeInjectionScenarios(t, pi)
	})

	// Desktop injection scenarios (MCP + inject-trap, no external auth needed)
	t.Run("Desktop", func(t *testing.T) {
		runDesktopInjectionScenarios(t)
	})

	// Copilot injection scenarios (require auth + CONNECT tunnel)
	if liveCopilotAuth != nil {
		caPool := loadProxyCA(t, homeDir)
		client := copilotHTTPClient(t, fmt.Sprintf("127.0.0.1:%d", pi.port), caPool)

		for _, model := range copilotModels {
			model := model // capture
			t.Run(model.Name, func(t *testing.T) {
				runCopilotInjectionScenarios(t, pi, model.Name, model.ModelID, client)
			})
		}
	} else {
		// Record SKIP for all Copilot injection scenarios
		for _, model := range copilotModels {
			liveResults.record(model.Name, "Injection", "SKIP")
			liveResults.record(model.Name, "Approve", "SKIP")
			liveResults.record(model.Name, "Reject", "SKIP")
		}
	}
}

func TestLivePassthrough(t *testing.T) {
	if liveAPIToken == "" {
		t.Skip("AEGIS_API_TOKEN not set, skipping live tests")
	}

	homeDir := t.TempDir()
	port := liveFindFreePort(t)
	pi := liveStartProxy(t, liveBinaryPath, homeDir, port, false, liveDashboardURL, liveAPIToken)

	// Claude passthrough/clean scenarios
	t.Run("Claude", func(t *testing.T) {
		runClaudePassthroughScenarios(t, pi)
	})

	// Desktop passthrough/clean scenarios (MCP, no external auth needed)
	t.Run("Desktop", func(t *testing.T) {
		runDesktopPassthroughScenarios(t, pi)
	})

	// Copilot passthrough/clean scenarios
	if liveCopilotAuth != nil {
		caPool := loadProxyCA(t, homeDir)
		client := copilotHTTPClient(t, fmt.Sprintf("127.0.0.1:%d", pi.port), caPool)

		for _, model := range copilotModels {
			model := model // capture
			t.Run(model.Name, func(t *testing.T) {
				runCopilotPassthroughScenarios(t, pi, model.Name, model.ModelID, client)
			})
		}
	} else {
		// Record SKIP for all Copilot passthrough scenarios
		for _, model := range copilotModels {
			liveResults.record(model.Name, "Passthrough", "SKIP")
			liveResults.record(model.Name, "Clean", "SKIP")
		}
	}
}
