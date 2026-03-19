package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agentsaegis/go-proxy/internal/client"
	"github.com/agentsaegis/go-proxy/internal/config"
	"github.com/agentsaegis/go-proxy/internal/trap"
)

func makeTestServer(t *testing.T) *Server {
	t.Helper()

	templates := []*trap.Template{
		{
			ID:           "trap_test",
			Category:     "destructive",
			Severity:     "critical",
			Triggers:     trap.Triggers{Keywords: []string{"rm"}},
			TrapCommands: []string{"rm -rf ./"},
			Training:     trap.Training{Title: "Test"},
		},
	}

	cfg := &config.Config{
		ProxyPort:        0, // port 0 for tests
		AnthropicBaseURL: "http://localhost:0",
		DashboardURL:     "http://localhost:0",
	}

	engine := trap.NewEngine(trap.DefaultOrgConfig())
	selector := trap.NewSelector(templates)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	apiClient := client.New("http://localhost:0", "tok_test")

	return New(cfg, engine, selector, apiClient, logger)
}

func TestNew(t *testing.T) {
	s := makeTestServer(t)
	if s == nil {
		t.Fatal("New() returned nil")
	}
	if s.httpServer == nil {
		t.Error("httpServer is nil")
	}
	if s.proxyHandler == nil {
		t.Error("proxyHandler is nil")
	}
	if s.callbackHandler == nil {
		t.Error("callbackHandler is nil")
	}
	if s.logger == nil {
		t.Error("logger is nil")
	}
}

func TestServer_HandleHealth(t *testing.T) {
	s := makeTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/__aegis/health", http.NoBody)
	rr := httptest.NewRecorder()

	s.handleHealth(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	if rr.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", rr.Header().Get("Content-Type"))
	}

	var response map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if response["status"] != "ok" {
		t.Errorf("status = %q, want %q", response["status"], "ok")
	}
}

func TestServer_Shutdown(t *testing.T) {
	s := makeTestServer(t)

	// Start in a goroutine
	go func() {
		_ = s.Start()
	}()

	// Give the server a moment to start
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := s.Shutdown(ctx)
	if err != nil {
		t.Errorf("Shutdown() error = %v", err)
	}
}

func TestServer_HandleProxy_Routes(t *testing.T) {
	s := makeTestServer(t)

	// Test that handleProxy delegates to proxyHandler
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", http.NoBody)
	rr := httptest.NewRecorder()

	// This will fail because the upstream is unreachable, but it shows routing works
	s.handleProxy(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d (upstream unreachable)", rr.Code, http.StatusBadGateway)
	}
}

func TestServer_SetSuperDebug(t *testing.T) {
	s := makeTestServer(t)

	s.SetSuperDebug()

	if s.hookHandler.maxCooldown != 0 {
		t.Errorf("maxCooldown = %d, want 0 after SetSuperDebug", s.hookHandler.maxCooldown)
	}
	if !s.hookHandler.disableJitter {
		t.Error("disableJitter should be true after SetSuperDebug")
	}
}

func TestServer_HandleHook_Routes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	s := makeTestServer(t)

	body := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"echo hello"},"tool_use_id":"toolu_test"}`
	req := httptest.NewRequest(http.MethodPost, "/hooks/pre-tool-use", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	s.handleHook(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestServer_New_WithHookSecret(t *testing.T) {
	cfg := &config.Config{
		ProxyPort:        0,
		AnthropicBaseURL: "http://localhost:0",
	}

	engine := trap.NewEngine(trap.DefaultOrgConfig())
	selector := trap.NewSelector([]*trap.Template{
		{
			ID:           "trap_test",
			Category:     "destructive",
			Severity:     "critical",
			Triggers:     trap.Triggers{Keywords: []string{"rm"}},
			TrapCommands: []string{"rm -rf ./"},
			Training:     trap.Training{Title: "Test"},
		},
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	s := New(cfg, engine, selector, nil, logger, "my-secret")
	if s.hookHandler.hookSecret != "my-secret" {
		t.Errorf("hookSecret = %q, want %q", s.hookHandler.hookSecret, "my-secret")
	}
}

func TestServer_New_WithPort(t *testing.T) {
	cfg := &config.Config{
		ProxyPort:        9876,
		AnthropicBaseURL: "http://localhost:0",
	}

	engine := trap.NewEngine(trap.DefaultOrgConfig())
	selector := trap.NewSelector([]*trap.Template{
		{
			ID:           "trap_test",
			Category:     "destructive",
			Severity:     "critical",
			Triggers:     trap.Triggers{Keywords: []string{"rm"}},
			TrapCommands: []string{"rm -rf ./"},
			Training:     trap.Training{Title: "Test"},
		},
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	s := New(cfg, engine, selector, nil, logger)
	if s.httpServer.Addr != ":9876" {
		t.Errorf("Addr = %q, want %q", s.httpServer.Addr, ":9876")
	}
}
