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

// ---------------------------------------------------------------------------
// Package-level vars (set in TestMain)
// ---------------------------------------------------------------------------

var (
	liveBinaryPath   string
	liveAPIToken     string
	liveDashboardURL string
	liveCopilotAuth  *copilotAuth
)

// ---------------------------------------------------------------------------
// Result matrix
// ---------------------------------------------------------------------------

var providers = []string{"Claude", "Copilot/GPT", "Copilot/Claude", "Copilot/Codex"}
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
	header := fmt.Sprintf("%-16s", "Provider")
	for _, s := range scenarios {
		header += fmt.Sprintf(" | %-12s", s)
	}
	fmt.Println(header)
	fmt.Println(strings.Repeat("-", len(header)))

	// Data rows
	tested, passed, failed := 0, 0, 0
	for _, p := range providers {
		row := fmt.Sprintf("%-16s", p)
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
	liveDashboardURL = os.Getenv("AEGIS_DASHBOARD_URL")
	if liveDashboardURL == "" {
		liveDashboardURL = "https://api.agentsaegis.com"
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
// It logs errors to stderr and returns nil on failure instead of calling t.Fatalf.
func acquireCopilotTokenForMain() *copilotAuth {
	ghToken := os.Getenv("GITHUB_TOKEN")
	if ghToken == "" {
		out, err := exec.Command("gh", "auth", "token").Output()
		if err != nil {
			fmt.Fprintln(os.Stderr, "acquireCopilotTokenForMain: no GITHUB_TOKEN and gh auth failed, skipping Copilot tests")
			return nil
		}
		ghToken = strings.TrimSpace(string(out))
	}
	if ghToken == "" {
		return nil
	}

	req, err := http.NewRequest(http.MethodGet,
		"https://api.github.com/copilot_internal/v2/token", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "acquireCopilotTokenForMain: build request: %v\n", err)
		return nil
	}
	req.Header.Set("Authorization", "token "+ghToken)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "acquireCopilotTokenForMain: exchange request: %v\n", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "acquireCopilotTokenForMain: token exchange returned %d: %s\n", resp.StatusCode, body)
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
		fmt.Fprintf(os.Stderr, "acquireCopilotTokenForMain: decode response: %v\n", err)
		return nil
	}
	if result.Token == "" {
		fmt.Fprintln(os.Stderr, "acquireCopilotTokenForMain: empty token in response")
		return nil
	}

	endpoint := result.Endpoints.API
	if endpoint == "" {
		endpoint = "https://api.individual.githubcopilot.com"
	}

	return &copilotAuth{Token: result.Token, Endpoint: endpoint}
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
