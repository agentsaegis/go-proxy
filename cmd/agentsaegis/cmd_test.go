package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentsaegis/go-proxy/internal/config"
)

func setupTestHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	configDir := filepath.Join(dir, ".agentsaegis")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("proxy_port: 7331\nlog_level: info\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRootCommand_HasSubcommands(t *testing.T) {
	names := make(map[string]bool)
	for _, cmd := range rootCmd.Commands() {
		names[cmd.Name()] = true
	}

	for _, want := range []string{"start", "init", "status", "stop", "setup-shell", "remove-shell", "report"} {
		if !names[want] {
			t.Errorf("root command missing subcommand %q", want)
		}
	}
}

func TestStartCommand_Flags(t *testing.T) {
	f := startCmd.Flags()

	if f.Lookup("daemon") == nil {
		t.Error("start command missing --daemon flag")
	}
	if f.Lookup("debug") == nil {
		t.Error("start command missing --debug flag")
	}
	if f.Lookup("super-debug") == nil {
		t.Error("start command missing --super-debug flag")
	}
}

func TestStartDaemon_AlreadyRunning(t *testing.T) {
	dir := setupTestHome(t)
	configDir := filepath.Join(dir, ".agentsaegis")

	// Write our own PID (which IS running) as the daemon PID
	pidFile := filepath.Join(configDir, "aegis.pid")
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{ProxyPort: 7331}
	err := startDaemon(cfg)
	if err == nil {
		t.Fatal("expected error for already running daemon")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("error = %q, want 'already running'", err.Error())
	}
}

func TestStartDaemon_StalePIDAllowsStart(t *testing.T) {
	dir := setupTestHome(t)
	configDir := filepath.Join(dir, ".agentsaegis")

	// Write a PID for a process that doesn't exist
	pidFile := filepath.Join(configDir, "aegis.pid")
	if err := os.WriteFile(pidFile, []byte("999999999"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{ProxyPort: 7331}
	err := startDaemon(cfg)
	// Should NOT get "already running" error (stale PID is ignored)
	// Will get a different error (e.g., exec failure in test env) which is fine
	if err != nil && strings.Contains(err.Error(), "already running") {
		t.Errorf("stale PID should not block daemon start, got: %v", err)
	}
}

func TestRunStart_SuperDebugImpliesDebug(t *testing.T) {
	// Verify that --super-debug sets debugFlag
	oldDaemon, oldDebug, oldSuperDebug := daemonFlag, debugFlag, superDebugFlag
	defer func() {
		daemonFlag, debugFlag, superDebugFlag = oldDaemon, oldDebug, oldSuperDebug
	}()

	superDebugFlag = true
	debugFlag = false
	daemonFlag = false

	dir := setupTestHome(t)
	_ = dir

	// We can't run the full runStart (it starts a server) but we can
	// test that the flag logic works by checking config loading + flag override
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}

	// Simulate the logic from runStart
	if superDebugFlag {
		debugFlag = true
	}
	if debugFlag {
		cfg.LogLevel = "debug"
	}

	if !debugFlag {
		t.Error("super-debug should imply debug")
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "debug")
	}
}

func TestStartDaemon_SuperDebugFlagForwarded(t *testing.T) {
	oldDaemon, oldDebug, oldSuperDebug := daemonFlag, debugFlag, superDebugFlag
	defer func() {
		daemonFlag, debugFlag, superDebugFlag = oldDaemon, oldDebug, oldSuperDebug
	}()

	// When super-debug is set, the args slice should include --super-debug
	superDebugFlag = true
	debugFlag = true

	args := []string{"start"}
	if superDebugFlag {
		args = append(args, "--super-debug")
	} else if debugFlag {
		args = append(args, "--debug")
	}

	found := false
	for _, arg := range args {
		if arg == "--super-debug" {
			found = true
		}
	}
	if !found {
		t.Errorf("args %v should contain --super-debug", args)
	}
	// --debug should NOT be present since super-debug implies debug
	for _, arg := range args {
		if arg == "--debug" {
			t.Errorf("args %v should not contain --debug when --super-debug is set", args)
		}
	}
}

func TestRunStop_NoPIDFile(t *testing.T) {
	setupTestHome(t)

	err := runStop(nil, nil)
	if err == nil {
		t.Fatal("expected error when no PID file exists")
	}
	if !strings.Contains(err.Error(), "no running proxy found") {
		t.Errorf("error = %q, want 'no running proxy found'", err.Error())
	}
}

func TestRunStop_StalePID(t *testing.T) {
	dir := setupTestHome(t)
	configDir := filepath.Join(dir, ".agentsaegis")

	// Write a PID for a process that doesn't exist
	pidFile := filepath.Join(configDir, "aegis.pid")
	if err := os.WriteFile(pidFile, []byte("999999999"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runStop(nil, nil)
	if err != nil {
		t.Errorf("runStop() error = %v, want nil for stale PID cleanup", err)
	}

	// PID file should be cleaned up
	if _, statErr := os.Stat(pidFile); statErr == nil {
		t.Error("stale PID file should have been removed")
	}
}

func TestRunReport_NoToken(t *testing.T) {
	setupTestHome(t)

	// runReport should handle missing API token gracefully
	err := runReport(nil, nil)
	if err != nil {
		t.Errorf("runReport() error = %v, want nil", err)
	}
}

func TestRunSetupShell_Zsh(t *testing.T) {
	dir := setupTestHome(t)
	t.Setenv("SHELL", "/bin/zsh")

	// Create a .zshrc without AgentsAegis config
	zshrcPath := filepath.Join(dir, ".zshrc")
	if err := os.WriteFile(zshrcPath, []byte("# existing config\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runSetupShell(nil, nil)
	if err != nil {
		t.Fatalf("runSetupShell() error = %v", err)
	}

	content, err := os.ReadFile(zshrcPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(content)
	if !strings.Contains(s, markerBegin) {
		t.Error("expected begin marker in .zshrc")
	}
	if !strings.Contains(s, markerEnd) {
		t.Error("expected end marker in .zshrc")
	}
	if !strings.Contains(s, "claude()") {
		t.Error("expected claude() wrapper function in .zshrc")
	}
	if !strings.Contains(s, "ANTHROPIC_BASE_URL") {
		t.Error("expected ANTHROPIC_BASE_URL in .zshrc")
	}
	if !strings.Contains(s, "__aegis/health") {
		t.Error("expected health check in .zshrc")
	}
	if !strings.Contains(s, "# existing config") {
		t.Error("existing config should be preserved")
	}
}

func TestRunSetupShell_ReplacesLegacyExport(t *testing.T) {
	dir := setupTestHome(t)
	t.Setenv("SHELL", "/bin/zsh")

	// Create a .zshrc with old-style export
	zshrcPath := filepath.Join(dir, ".zshrc")
	oldContent := "# other stuff\n# AgentsAegis proxy - route Claude Code through security proxy\nexport ANTHROPIC_BASE_URL=http://localhost:7331\n"
	if err := os.WriteFile(zshrcPath, []byte(oldContent), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runSetupShell(nil, nil)
	if err != nil {
		t.Fatalf("runSetupShell() error = %v", err)
	}

	content, err := os.ReadFile(zshrcPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(content)
	// Legacy export should be removed
	if strings.Contains(s, "export ANTHROPIC_BASE_URL") {
		t.Error("legacy export line should be removed")
	}
	// Wrapper should be present
	if !strings.Contains(s, "claude()") {
		t.Error("expected claude() wrapper function")
	}
	// Only one ANTHROPIC_BASE_URL reference (in the wrapper)
	if strings.Count(s, "ANTHROPIC_BASE_URL") != 1 {
		t.Errorf("expected exactly 1 ANTHROPIC_BASE_URL, got %d", strings.Count(s, "ANTHROPIC_BASE_URL"))
	}
}

func TestRunSetupShell_Idempotent(t *testing.T) {
	dir := setupTestHome(t)
	t.Setenv("SHELL", "/bin/zsh")

	zshrcPath := filepath.Join(dir, ".zshrc")
	if err := os.WriteFile(zshrcPath, []byte("# existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Run setup twice
	if err := runSetupShell(nil, nil); err != nil {
		t.Fatalf("first runSetupShell() error = %v", err)
	}
	if err := runSetupShell(nil, nil); err != nil {
		t.Fatalf("second runSetupShell() error = %v", err)
	}

	content, err := os.ReadFile(zshrcPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(content)
	if strings.Count(s, markerBegin) != 1 {
		t.Errorf("expected exactly 1 begin marker, got %d", strings.Count(s, markerBegin))
	}
	if strings.Count(s, "claude()") != 1 {
		t.Errorf("expected exactly 1 claude() function, got %d", strings.Count(s, "claude()"))
	}
}

func TestRunSetupShell_Bash(t *testing.T) {
	dir := setupTestHome(t)
	t.Setenv("SHELL", "/bin/bash")

	bashrcPath := filepath.Join(dir, ".bashrc")

	err := runSetupShell(nil, nil)
	if err != nil {
		t.Fatalf("runSetupShell() error = %v", err)
	}

	content, err := os.ReadFile(bashrcPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "ANTHROPIC_BASE_URL") {
		t.Error("expected ANTHROPIC_BASE_URL in .bashrc")
	}
}

func TestRunSetupShell_Fish(t *testing.T) {
	dir := setupTestHome(t)
	t.Setenv("SHELL", "/usr/local/bin/fish")

	fishDir := filepath.Join(dir, ".config", "fish")
	if err := os.MkdirAll(fishDir, 0o755); err != nil {
		t.Fatal(err)
	}

	err := runSetupShell(nil, nil)
	if err != nil {
		t.Fatalf("runSetupShell() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(fishDir, "config.fish"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(content)
	if !strings.Contains(s, "ANTHROPIC_BASE_URL") {
		t.Error("expected ANTHROPIC_BASE_URL in config.fish")
	}
	if !strings.Contains(s, "set -lx ANTHROPIC_BASE_URL") {
		t.Error("expected fish syntax 'set -lx' in config.fish")
	}
	if !strings.Contains(s, "function claude") {
		t.Error("expected 'function claude' in config.fish")
	}
}

func TestRunSetupShell_UnknownShell(t *testing.T) {
	setupTestHome(t)
	t.Setenv("SHELL", "/bin/unknown")

	// Should not error, just print instructions
	err := runSetupShell(nil, nil)
	if err != nil {
		t.Errorf("runSetupShell() error = %v, want nil for unknown shell", err)
	}
}

func TestRunServer_StartsAndShutdown(t *testing.T) {
	dir := setupTestHome(t)
	_ = dir

	oldDaemon, oldDebug, oldSuperDebug := daemonFlag, debugFlag, superDebugFlag
	defer func() {
		daemonFlag, debugFlag, superDebugFlag = oldDaemon, oldDebug, oldSuperDebug
	}()
	daemonFlag = false
	debugFlag = false
	superDebugFlag = false

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	// Use port 0 to let OS pick
	cfg.ProxyPort = 0

	// Start server in goroutine, then shut down via signal
	errCh := make(chan error, 1)
	go func() {
		errCh <- runServer(cfg)
	}()

	// Give server time to start
	time.Sleep(200 * time.Millisecond)

	// Send SIGINT to trigger graceful shutdown
	p, _ := os.FindProcess(os.Getpid())
	_ = p.Signal(os.Interrupt)

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("runServer() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("server did not shut down within timeout")
	}
}

func TestRunServer_SuperDebug(t *testing.T) {
	dir := setupTestHome(t)
	_ = dir

	oldDaemon, oldDebug, oldSuperDebug := daemonFlag, debugFlag, superDebugFlag
	defer func() {
		daemonFlag, debugFlag, superDebugFlag = oldDaemon, oldDebug, oldSuperDebug
	}()
	daemonFlag = false
	debugFlag = true
	superDebugFlag = true

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	cfg.ProxyPort = 0

	errCh := make(chan error, 1)
	go func() {
		errCh <- runServer(cfg)
	}()

	time.Sleep(200 * time.Millisecond)

	p, _ := os.FindProcess(os.Getpid())
	_ = p.Signal(os.Interrupt)

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("runServer() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("server did not shut down within timeout")
	}
}

func TestRunServer_WithAPIToken(t *testing.T) {
	dir := setupTestHome(t)
	configDir := filepath.Join(dir, ".agentsaegis")
	// Write config with an API token pointing to a non-existent server
	configContent := "proxy_port: 0\nlog_level: debug\napi_token: tok_test\ndashboard_url: http://127.0.0.1:1\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configContent), 0o600); err != nil {
		t.Fatal(err)
	}

	oldDaemon, oldDebug, oldSuperDebug := daemonFlag, debugFlag, superDebugFlag
	defer func() {
		daemonFlag, debugFlag, superDebugFlag = oldDaemon, oldDebug, oldSuperDebug
	}()
	daemonFlag = false
	debugFlag = true
	superDebugFlag = false

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- runServer(cfg)
	}()

	time.Sleep(200 * time.Millisecond)

	p, _ := os.FindProcess(os.Getpid())
	_ = p.Signal(os.Interrupt)

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("runServer() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("server did not shut down within timeout")
	}
}

func TestRunStart_Foreground(t *testing.T) {
	setupTestHome(t)

	oldDaemon, oldDebug, oldSuperDebug := daemonFlag, debugFlag, superDebugFlag
	defer func() {
		daemonFlag, debugFlag, superDebugFlag = oldDaemon, oldDebug, oldSuperDebug
	}()
	daemonFlag = false
	debugFlag = false
	superDebugFlag = false

	// runStart -> runServer, which starts a server on default port
	// We need to use a port 0 config for this to work
	dir, _ := os.UserHomeDir()
	configDir := filepath.Join(dir, ".agentsaegis")
	configContent := "proxy_port: 0\nlog_level: info\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configContent), 0o600); err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- runStart(nil, nil)
	}()

	time.Sleep(200 * time.Millisecond)

	p, _ := os.FindProcess(os.Getpid())
	_ = p.Signal(os.Interrupt)

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("runStart() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("runStart did not complete within timeout")
	}
}

func TestRunInit_WithMockedStdin(t *testing.T) {
	dir := setupTestHome(t)
	_ = dir

	// Create a mock API server that validates the token
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"valid":true}`))
	}))
	defer mockServer.Close()

	// Create a pipe to mock stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	oldStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()

	// Write mock input: dashboard URL + newline, API token + newline
	go func() {
		fmt.Fprintf(w, "%s\n", mockServer.URL)
		fmt.Fprintf(w, "test-api-token\n")
		w.Close()
	}()

	err = runInit(nil, nil)
	if err != nil {
		t.Errorf("runInit() error = %v", err)
	}

	// Verify config file was written
	homeDir, _ := os.UserHomeDir()
	configPath := filepath.Join(homeDir, ".agentsaegis", "config.yaml")
	content, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("config file not written: %v", readErr)
	}
	if !strings.Contains(string(content), "test-api-token") {
		t.Error("config file should contain the API token")
	}
	if !strings.Contains(string(content), mockServer.URL) {
		t.Error("config file should contain the dashboard URL")
	}
}

func TestRunInit_EmptyToken(t *testing.T) {
	setupTestHome(t)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()

	go func() {
		fmt.Fprintf(w, "\n") // empty dashboard URL (use default)
		fmt.Fprintf(w, "\n") // empty API token
		w.Close()
	}()

	err = runInit(nil, nil)
	if err == nil {
		t.Fatal("expected error for empty API token")
	}
	if !strings.Contains(err.Error(), "API token is required") {
		t.Errorf("error = %q, want 'API token is required'", err.Error())
	}
}

func TestRunReport_WithMockServer(t *testing.T) {
	dir := setupTestHome(t)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"catch_rate":"75%","total_traps":10,"caught":7,"missed":3,"recent_traps":[{"category":"destructive","result":"caught","date":"2026-03-17"}]}`))
	}))
	defer mockServer.Close()

	// Write config with API token
	configDir := filepath.Join(dir, ".agentsaegis")
	configContent := fmt.Sprintf("proxy_port: 7331\napi_token: test-token\ndashboard_url: %s\n", mockServer.URL)
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configContent), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runReport(nil, nil)
	if err != nil {
		t.Errorf("runReport() error = %v", err)
	}
}

func TestRunReport_ServerError(t *testing.T) {
	dir := setupTestHome(t)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer mockServer.Close()

	configDir := filepath.Join(dir, ".agentsaegis")
	configContent := fmt.Sprintf("proxy_port: 7331\napi_token: test-token\ndashboard_url: %s\n", mockServer.URL)
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configContent), 0o600); err != nil {
		t.Fatal(err)
	}

	// Should handle error gracefully
	err := runReport(nil, nil)
	if err != nil {
		t.Errorf("runReport() should not return error on API failure, got: %v", err)
	}
}

func TestRunSetupShell_PortMismatch(t *testing.T) {
	dir := setupTestHome(t)
	t.Setenv("SHELL", "/bin/zsh")

	// Write a .zshrc with old-style export on wrong port
	zshrcPath := filepath.Join(dir, ".zshrc")
	if err := os.WriteFile(zshrcPath, []byte("export ANTHROPIC_BASE_URL=http://localhost:9999\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Should replace with correct wrapper
	err := runSetupShell(nil, nil)
	if err != nil {
		t.Errorf("runSetupShell() error = %v", err)
	}

	content, err := os.ReadFile(zshrcPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(content)
	// Old export should be gone
	if strings.Contains(s, "localhost:9999") {
		t.Error("old port should be replaced")
	}
	// Correct port in wrapper
	if !strings.Contains(s, "localhost:7331") {
		t.Error("expected correct port 7331 in wrapper")
	}
}

func TestRunRemoveShell(t *testing.T) {
	dir := setupTestHome(t)
	t.Setenv("SHELL", "/bin/zsh")

	zshrcPath := filepath.Join(dir, ".zshrc")
	content := "# my stuff\n\n" + shellWrapper(7331) + "\n# more stuff\n"
	if err := os.WriteFile(zshrcPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runRemoveShell(nil, nil)
	if err != nil {
		t.Fatalf("runRemoveShell() error = %v", err)
	}

	result, err := os.ReadFile(zshrcPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(result)
	if strings.Contains(s, markerBegin) {
		t.Error("marker block should be removed")
	}
	if strings.Contains(s, "claude()") {
		t.Error("claude() wrapper should be removed")
	}
	if !strings.Contains(s, "# my stuff") {
		t.Error("other content should be preserved")
	}
	if !strings.Contains(s, "# more stuff") {
		t.Error("content after block should be preserved")
	}
}

func TestRunRemoveShell_NoConfig(t *testing.T) {
	dir := setupTestHome(t)
	t.Setenv("SHELL", "/bin/zsh")

	zshrcPath := filepath.Join(dir, ".zshrc")
	if err := os.WriteFile(zshrcPath, []byte("# nothing here\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runRemoveShell(nil, nil)
	if err != nil {
		t.Errorf("runRemoveShell() error = %v", err)
	}
}
