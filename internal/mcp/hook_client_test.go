package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHookClient_Allow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	client := NewHookClient(srv.URL, "", testLogger())
	result, err := client.CheckCommand(context.Background(), "sess1", "ls -la", "toolu_123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Allowed {
		t.Error("expected allowed=true")
	}
}

func TestHookClient_Deny(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := hookResponse{
			HookSpecificOutput: &hookOutput{
				HookEventName:            "PreToolUse",
				PermissionDecision:       "deny",
				PermissionDecisionReason: "Command blocked by security policy.",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		data, _ := json.Marshal(resp)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	client := NewHookClient(srv.URL, "", testLogger())
	result, err := client.CheckCommand(context.Background(), "sess1", "rm -rf /", "toolu_123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Allowed {
		t.Error("expected allowed=false")
	}
	if result.Reason != "Command blocked by security policy." {
		t.Errorf("reason = %q, want %q", result.Reason, "Command blocked by security policy.")
	}
}

func TestHookClient_ConnectionRefused(t *testing.T) {
	// Use a URL that will not have a server listening
	client := NewHookClient("http://127.0.0.1:19999", "", testLogger())
	result, err := client.CheckCommand(context.Background(), "sess1", "ls", "")
	if err == nil {
		t.Fatal("expected error for connection refused")
	}
	if !result.Allowed {
		t.Error("expected fail-open (allowed=true) on connection error")
	}
}

func TestHookClient_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
	}))
	defer srv.Close()

	client := NewHookClient(srv.URL, "", testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	result, err := client.CheckCommand(ctx, "sess1", "ls", "")
	if err == nil {
		t.Fatal("expected error on timeout")
	}
	if !result.Allowed {
		t.Error("expected fail-open (allowed=true) on timeout")
	}
}

func TestHookClient_WithSecret(t *testing.T) {
	var receivedSecret string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSecret = r.Header.Get("X-Hook-Secret")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	client := NewHookClient(srv.URL, "my-secret-token", testLogger())
	_, err := client.CheckCommand(context.Background(), "sess1", "ls", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedSecret != "my-secret-token" {
		t.Errorf("X-Hook-Secret = %q, want %q", receivedSecret, "my-secret-token")
	}
}

func TestHookClient_MalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	client := NewHookClient(srv.URL, "", testLogger())
	result, err := client.CheckCommand(context.Background(), "sess1", "ls", "")
	// Malformed response should fail-open
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Allowed {
		t.Error("expected fail-open (allowed=true) on malformed response")
	}
}

func TestHookClient_RequestBody(t *testing.T) {
	var receivedReq hookRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &receivedReq)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	client := NewHookClient(srv.URL, "", testLogger())
	_, err := client.CheckCommand(context.Background(), "test-session", "git push --force", "toolu_abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if receivedReq.SessionID != "test-session" {
		t.Errorf("session_id = %q, want %q", receivedReq.SessionID, "test-session")
	}
	if receivedReq.HookEventName != "PreToolUse" {
		t.Errorf("hook_event_name = %q, want %q", receivedReq.HookEventName, "PreToolUse")
	}
	if receivedReq.ToolName != "Bash" {
		t.Errorf("tool_name = %q, want %q", receivedReq.ToolName, "Bash")
	}
	if receivedReq.ToolUseID != "toolu_abc" {
		t.Errorf("tool_use_id = %q, want %q", receivedReq.ToolUseID, "toolu_abc")
	}

	var toolInput struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(receivedReq.ToolInput, &toolInput); err != nil {
		t.Fatalf("parsing tool_input: %v", err)
	}
	if toolInput.Command != "git push --force" {
		t.Errorf("tool_input.command = %q, want %q", toolInput.Command, "git push --force")
	}
}
