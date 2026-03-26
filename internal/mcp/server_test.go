package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mockHookChecker implements HookChecker for testing.
type mockHookChecker struct {
	result *HookResult
	err    error
}

func (m *mockHookChecker) CheckCommand(_ context.Context, _, _, _ string) (*HookResult, error) {
	return m.result, m.err
}

func sendRequest(t *testing.T, stdin *bytes.Buffer, method string, id interface{}, params interface{}) {
	t.Helper()
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if id != nil {
		req["id"] = id
	}
	if params != nil {
		req["params"] = params
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshaling request: %v", err)
	}
	stdin.Write(data)
	stdin.WriteByte('\n')
}

func readResponse(t *testing.T, output string) *JSONRPCResponse {
	t.Helper()
	output = strings.TrimSpace(output)
	if output == "" {
		return nil
	}
	var resp JSONRPCResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("parsing response %q: %v", output, err)
	}
	return &resp
}

func readResponses(t *testing.T, output string) []*JSONRPCResponse {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var resps []*JSONRPCResponse
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var resp JSONRPCResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("parsing response %q: %v", line, err)
		}
		resps = append(resps, &resp)
	}
	return resps
}

func runServer(t *testing.T, checker HookChecker, input string) string {
	t.Helper()
	stdin := bytes.NewBufferString(input)
	var stdout bytes.Buffer
	srv := New(checker, testLogger(), stdin, &stdout)
	if err := srv.Run(context.Background()); err != nil {
		t.Fatalf("server.Run: %v", err)
	}
	return stdout.String()
}

func TestServer_Initialize(t *testing.T) {
	checker := &mockHookChecker{result: &HookResult{Allowed: true}}

	var stdin bytes.Buffer
	sendRequest(t, &stdin, "initialize", 1, map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})

	output := runServer(t, checker, stdin.String())
	resp := readResponse(t, output)

	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	resultJSON, _ := json.Marshal(resp.Result)
	var result initializeResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatalf("parsing result: %v", err)
	}

	if result.ServerInfo.Name != "agentsaegis" {
		t.Errorf("server name = %q, want %q", result.ServerInfo.Name, "agentsaegis")
	}
	if result.Capabilities.Tools == nil {
		t.Error("expected tools capability")
	}
}

func TestServer_ToolsList(t *testing.T) {
	checker := &mockHookChecker{result: &HookResult{Allowed: true}}

	var stdin bytes.Buffer
	sendRequest(t, &stdin, "tools/list", 1, nil)

	output := runServer(t, checker, stdin.String())
	resp := readResponse(t, output)

	if resp == nil {
		t.Fatal("expected response")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	resultJSON, _ := json.Marshal(resp.Result)
	var result toolsListResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatalf("parsing result: %v", err)
	}

	if len(result.Tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(result.Tools))
	}
	if result.Tools[0].Name != "bash" {
		t.Errorf("tool name = %q, want %q", result.Tools[0].Name, "bash")
	}
}

func TestServer_ToolsCall_Allow(t *testing.T) {
	checker := &mockHookChecker{result: &HookResult{Allowed: true}}

	var stdin bytes.Buffer
	sendRequest(t, &stdin, "tools/call", 1, map[string]interface{}{
		"name":      "bash",
		"arguments": map[string]string{"command": "echo hello-world"},
	})

	output := runServer(t, checker, stdin.String())
	resp := readResponse(t, output)

	if resp == nil {
		t.Fatal("expected response")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	resultJSON, _ := json.Marshal(resp.Result)
	var result ToolResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatalf("parsing result: %v", err)
	}

	if result.IsError {
		t.Error("expected isError=false for allowed command")
	}
	if len(result.Content) == 0 {
		t.Fatal("expected content")
	}
	if !strings.Contains(result.Content[0].Text, "hello-world") {
		t.Errorf("output = %q, want to contain %q", result.Content[0].Text, "hello-world")
	}
}

func TestServer_ToolsCall_Deny(t *testing.T) {
	checker := &mockHookChecker{result: &HookResult{Allowed: false, Reason: "trap detected"}}

	var stdin bytes.Buffer
	sendRequest(t, &stdin, "tools/call", 1, map[string]interface{}{
		"name":      "bash",
		"arguments": map[string]string{"command": "rm -rf /tmp/.aegis-trap-test"},
	})

	output := runServer(t, checker, stdin.String())
	resp := readResponse(t, output)

	if resp == nil {
		t.Fatal("expected response")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected RPC error: %v", resp.Error)
	}

	resultJSON, _ := json.Marshal(resp.Result)
	var result ToolResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatalf("parsing result: %v", err)
	}

	if !result.IsError {
		t.Error("expected isError=true for denied command")
	}
	if len(result.Content) == 0 {
		t.Fatal("expected content")
	}
	if !strings.Contains(result.Content[0].Text, "AGENTSAEGIS") {
		t.Error("expected training message in output")
	}
}

func TestServer_ToolsCall_ProxyDown_FallbackAllow(t *testing.T) {
	// Hook unreachable, no trap files -> command executes (fail-open)
	checker := &mockHookChecker{
		result: &HookResult{Allowed: true},
		err:    fmt.Errorf("connection refused"),
	}

	// Set HOME to empty temp dir (no trap files)
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	var stdin bytes.Buffer
	sendRequest(t, &stdin, "tools/call", 1, map[string]interface{}{
		"name":      "bash",
		"arguments": map[string]string{"command": "echo fallback-works"},
	})

	output := runServer(t, checker, stdin.String())
	resp := readResponse(t, output)

	if resp == nil {
		t.Fatal("expected response")
	}

	resultJSON, _ := json.Marshal(resp.Result)
	var result ToolResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatalf("parsing result: %v", err)
	}

	if result.IsError {
		t.Error("expected isError=false for fail-open")
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "fallback-works") {
		t.Error("expected command to execute in fail-open mode")
	}
}

func TestServer_ToolsCall_ProxyDown_FallbackDeny(t *testing.T) {
	// Hook unreachable, trap file matches -> blocked
	checker := &mockHookChecker{
		result: &HookResult{Allowed: true},
		err:    fmt.Errorf("connection refused"),
	}

	// Create a trap file in a temp dir
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	trapDir := filepath.Join(dir, ".agentsaegis", "traps")
	if err := os.MkdirAll(trapDir, 0o700); err != nil {
		t.Fatalf("creating trap dir: %v", err)
	}

	trapFile := map[string]interface{}{
		"id":           "trap_test_fallback",
		"trap_command": "rm -rf /tmp/.aegis-trap-test",
		"template_id":  "rm-rf",
		"category":     "destructive",
		"severity":     "critical",
		"injected_at":  time.Now().Format(time.RFC3339),
		"expires_at":   time.Now().Add(2 * time.Minute).Format(time.RFC3339),
	}
	data, _ := json.Marshal(trapFile)
	if err := os.WriteFile(filepath.Join(trapDir, "trap_test_fallback.json"), data, 0o600); err != nil {
		t.Fatalf("writing trap file: %v", err)
	}

	var stdin bytes.Buffer
	sendRequest(t, &stdin, "tools/call", 1, map[string]interface{}{
		"name":      "bash",
		"arguments": map[string]string{"command": "rm -rf /tmp/.aegis-trap-test"},
	})

	output := runServer(t, checker, stdin.String())
	resp := readResponse(t, output)

	if resp == nil {
		t.Fatal("expected response")
	}

	resultJSON, _ := json.Marshal(resp.Result)
	var result ToolResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatalf("parsing result: %v", err)
	}

	if !result.IsError {
		t.Error("expected isError=true for trap file fallback")
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "AGENTSAEGIS") {
		t.Error("expected training message from trap file fallback")
	}
}

func TestServer_ToolsCall_InvalidTool(t *testing.T) {
	checker := &mockHookChecker{result: &HookResult{Allowed: true}}

	var stdin bytes.Buffer
	sendRequest(t, &stdin, "tools/call", 1, map[string]interface{}{
		"name":      "unknown_tool",
		"arguments": map[string]string{},
	})

	output := runServer(t, checker, stdin.String())
	resp := readResponse(t, output)

	if resp == nil {
		t.Fatal("expected response")
	}
	if resp.Error == nil {
		t.Fatal("expected error for unknown tool")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("error code = %d, want -32602", resp.Error.Code)
	}
}

func TestServer_UnknownMethod(t *testing.T) {
	checker := &mockHookChecker{result: &HookResult{Allowed: true}}

	var stdin bytes.Buffer
	sendRequest(t, &stdin, "unknown/method", 1, nil)

	output := runServer(t, checker, stdin.String())
	resp := readResponse(t, output)

	if resp == nil {
		t.Fatal("expected response")
	}
	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("error code = %d, want -32601", resp.Error.Code)
	}
}

func TestServer_Notification(t *testing.T) {
	checker := &mockHookChecker{result: &HookResult{Allowed: true}}

	// Notification has no id field
	var stdin bytes.Buffer
	sendRequest(t, &stdin, "notifications/initialized", nil, nil)

	output := runServer(t, checker, stdin.String())
	if strings.TrimSpace(output) != "" {
		t.Errorf("expected no output for notification, got %q", output)
	}
}

func TestServer_MalformedJSON(t *testing.T) {
	checker := &mockHookChecker{result: &HookResult{Allowed: true}}

	output := runServer(t, checker, "this is not json\n")
	resp := readResponse(t, output)

	if resp == nil {
		t.Fatal("expected error response")
	}
	if resp.Error == nil {
		t.Fatal("expected parse error")
	}
	if resp.Error.Code != -32700 {
		t.Errorf("error code = %d, want -32700", resp.Error.Code)
	}
}

func TestServer_MultipleRequests(t *testing.T) {
	checker := &mockHookChecker{result: &HookResult{Allowed: true}}

	var stdin bytes.Buffer
	sendRequest(t, &stdin, "initialize", 1, map[string]interface{}{
		"protocolVersion": "2024-11-05",
	})
	sendRequest(t, &stdin, "tools/list", 2, nil)

	output := runServer(t, checker, stdin.String())
	resps := readResponses(t, output)

	if len(resps) != 2 {
		t.Fatalf("got %d responses, want 2", len(resps))
	}
}

func TestServer_ToolsCall_EmptyCommand(t *testing.T) {
	checker := &mockHookChecker{result: &HookResult{Allowed: true}}

	var stdin bytes.Buffer
	sendRequest(t, &stdin, "tools/call", 1, map[string]interface{}{
		"name":      "bash",
		"arguments": map[string]string{"command": ""},
	})

	output := runServer(t, checker, stdin.String())
	resp := readResponse(t, output)

	if resp == nil {
		t.Fatal("expected response")
	}
	if resp.Error == nil {
		t.Fatal("expected error for empty command")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("error code = %d, want -32602", resp.Error.Code)
	}
}

func TestServer_ToolsCall_CommandError(t *testing.T) {
	checker := &mockHookChecker{result: &HookResult{Allowed: true}}

	var stdin bytes.Buffer
	sendRequest(t, &stdin, "tools/call", 1, map[string]interface{}{
		"name":      "bash",
		"arguments": map[string]string{"command": "exit 1"},
	})

	output := runServer(t, checker, stdin.String())
	resp := readResponse(t, output)

	if resp == nil {
		t.Fatal("expected response")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected RPC error: %v", resp.Error)
	}

	resultJSON, _ := json.Marshal(resp.Result)
	var result ToolResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatalf("parsing result: %v", err)
	}

	if !result.IsError {
		t.Error("expected isError=true for failed command")
	}
}
