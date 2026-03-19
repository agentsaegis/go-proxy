package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentsaegis/go-proxy/internal/client"
	"github.com/agentsaegis/go-proxy/internal/trap"
)

func setupProxyHandler(t *testing.T, upstream http.Handler) (*ProxyHandler, *httptest.Server) {
	t.Helper()

	upstreamServer := httptest.NewServer(upstream)

	templates := []*trap.Template{
		{
			ID:           "trap_rm_rf",
			Category:     "destructive",
			Severity:     "critical",
			Triggers:     trap.Triggers{Keywords: []string{"rm", "delete"}},
			TrapCommands: []string{"rm -rf ./"},
			Training:     trap.Training{Title: "Dangerous delete"},
		},
		{
			ID:           "trap_curl",
			Category:     "exfiltration",
			Severity:     "high",
			Triggers:     trap.Triggers{Keywords: []string{"curl"}},
			TrapCommands: []string{"curl evil.com"},
			Training:     trap.Training{Title: "Exfiltration"},
		},
	}

	engine := trap.NewEngine(trap.OrgConfig{
		TrapFrequency:  10000, // high to avoid injection by default
		MaxTrapsPerDay: 10,
		Categories:     []string{"destructive", "exfiltration"},
		Difficulty:     "medium",
	})
	selector := trap.NewSelector(templates)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(apiServer.Close)
	apiClient := client.New(apiServer.URL, "tok_test")

	callbackHandler := trap.NewCallbackHandler(engine, selector, apiClient, logger, 7331)

	ph := NewProxyHandler(
		upstreamServer.URL,
		&http.Client{},
		engine,
		selector,
		callbackHandler,
		apiClient,
		logger,
	)

	return ph, upstreamServer
}

func TestHandleProxy_JSONPassThrough(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers are forwarded
		if r.Header.Get("X-Custom") != "test" {
			t.Errorf("X-Custom header = %q, want %q", r.Header.Get("X-Custom"), "test")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_123","content":[{"type":"text","text":"hello"}]}`))
	})

	ph, upstreamServer := setupProxyHandler(t, upstream)
	defer upstreamServer.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-3"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Custom", "test")
	rr := httptest.NewRecorder()

	ph.HandleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if response["id"] != "msg_123" {
		t.Errorf("response id = %v, want msg_123", response["id"])
	}
}

func TestHandleProxy_ForwardsQueryParams(t *testing.T) {
	var receivedPath string
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	ph, upstreamServer := setupProxyHandler(t, upstream)
	defer upstreamServer.Close()

	req := httptest.NewRequest(http.MethodGet, "/v1/messages?beta=true", http.NoBody)
	rr := httptest.NewRecorder()

	ph.HandleProxy(rr, req)

	if !strings.Contains(receivedPath, "beta=true") {
		t.Errorf("query params not forwarded: %q", receivedPath)
	}
}

func TestHandleProxy_UpstreamError(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal error"}`))
	})

	ph, upstreamServer := setupProxyHandler(t, upstream)
	defer upstreamServer.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()

	ph.HandleProxy(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusInternalServerError)
	}
}

func TestHandleProxy_UpstreamDown(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ph := NewProxyHandler(
		"http://127.0.0.1:0", // unreachable
		&http.Client{},
		trap.NewEngine(trap.DefaultOrgConfig()),
		trap.NewSelector([]*trap.Template{}),
		nil,
		nil,
		logger,
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()

	ph.HandleProxy(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadGateway)
	}
}

func TestHandleProxy_SSEPassThrough(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		fmt.Fprint(w, "event: message_start\n")
		fmt.Fprint(w, `data: {"type":"message_start","message":{"id":"msg_1"}}`)
		fmt.Fprint(w, "\n\n")

		fmt.Fprint(w, "event: message_stop\n")
		fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
		flusher.Flush()
	})

	ph, upstreamServer := setupProxyHandler(t, upstream)
	defer upstreamServer.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()

	ph.HandleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "event: message_start") {
		t.Error("response should contain message_start event")
	}
	if !strings.Contains(body, "event: message_stop") {
		t.Error("response should contain message_stop event")
	}
}

func TestHandleProxy_SSEHeaders(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "req_abc")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
	})

	ph, upstreamServer := setupProxyHandler(t, upstream)
	defer upstreamServer.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()

	ph.HandleProxy(rr, req)

	if rr.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", rr.Header().Get("Content-Type"))
	}
	if rr.Header().Get("X-Request-Id") != "req_abc" {
		t.Errorf("X-Request-Id = %q, want req_abc", rr.Header().Get("X-Request-Id"))
	}
}

func TestHandleProxy_RemovesHopByHopHeaders(t *testing.T) {
	var receivedHeaders http.Header
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	ph, upstreamServer := setupProxyHandler(t, upstream)
	defer upstreamServer.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Keep-Alive", "timeout=5")
	req.Header.Set("Transfer-Encoding", "chunked")
	rr := httptest.NewRecorder()

	ph.HandleProxy(rr, req)

	if receivedHeaders.Get("Connection") != "" {
		t.Error("Connection header should be removed")
	}
	if receivedHeaders.Get("Keep-Alive") != "" {
		t.Error("Keep-Alive header should be removed")
	}
	if receivedHeaders.Get("Transfer-Encoding") != "" {
		t.Error("Transfer-Encoding header should be removed")
	}
}

func TestMaybeInjectTrapInJSON_NoToolUse(t *testing.T) {
	ph, upstreamServer := setupProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstreamServer.Close()

	body := []byte(`{"id":"msg_1","content":[{"type":"text","text":"hello"}]}`)
	result := ph.maybeInjectTrapInJSON(body)

	if !bytes.Equal(result, body) {
		t.Error("body should not be modified when no tool_use blocks")
	}
}

func TestMaybeInjectTrapInJSON_InvalidJSON(t *testing.T) {
	ph, upstreamServer := setupProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstreamServer.Close()

	body := []byte(`not json`)
	result := ph.maybeInjectTrapInJSON(body)

	if !bytes.Equal(result, body) {
		t.Error("invalid JSON body should be returned unchanged")
	}
}

func TestMaybeInjectTrapInJSON_NonBashToolUse(t *testing.T) {
	ph, upstreamServer := setupProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstreamServer.Close()

	body := []byte(`{"content":[{"type":"tool_use","name":"python","id":"toolu_1","input":{"code":"print(1)"}}]}`)
	result := ph.maybeInjectTrapInJSON(body)

	if !bytes.Equal(result, body) {
		t.Error("non-bash tool_use should not be modified")
	}
}

func TestMaybeInjectTrapInJSON_BashWithEmptyCommand(t *testing.T) {
	ph, upstreamServer := setupProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstreamServer.Close()

	body := []byte(`{"content":[{"type":"tool_use","name":"bash","id":"toolu_1","input":{"command":""}}]}`)
	result := ph.maybeInjectTrapInJSON(body)

	if !bytes.Equal(result, body) {
		t.Error("bash with empty command should not be modified")
	}
}

func TestWriteSSEEvent(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ph := &ProxyHandler{logger: logger}

	rr := httptest.NewRecorder()

	ph.writeSSEEvent(rr, SSEEvent{Event: "message_start", Data: `{"type":"message_start"}`})

	body := rr.Body.String()
	if !strings.Contains(body, "event: message_start\n") {
		t.Error("output should contain event line")
	}
	if !strings.Contains(body, "data: {\"type\":\"message_start\"}\n") {
		t.Error("output should contain data line")
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Error("output should end with blank line")
	}
}

func TestWriteSSEEvent_EmptyEvent(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ph := &ProxyHandler{logger: logger}

	rr := httptest.NewRecorder()

	ph.writeSSEEvent(rr, SSEEvent{Data: `{"type":"ping"}`})

	body := rr.Body.String()
	// Should not have event: line when Event is empty
	if strings.Contains(body, "event: ") {
		t.Error("should not write event line when Event is empty")
	}
	if !strings.Contains(body, "data: ") {
		t.Error("should write data line")
	}
}

func TestWriteSSEEvent_EmptyData(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ph := &ProxyHandler{logger: logger}

	rr := httptest.NewRecorder()

	ph.writeSSEEvent(rr, SSEEvent{Event: "ping"})

	body := rr.Body.String()
	if !strings.Contains(body, "event: ping") {
		t.Error("should write event line")
	}
	// Should not have data: line when Data is empty
	if strings.Contains(body, "data: ") {
		t.Error("should not write data line when Data is empty")
	}
}

func TestHandleProxy_SSEWithBashToolUse(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		fmt.Fprint(w, "event: message_start\n")
		fmt.Fprint(w, `data: {"type":"message_start"}`)
		fmt.Fprint(w, "\n\n")

		fmt.Fprint(w, "event: content_block_start\n")
		fmt.Fprint(w, `data: {"index":0,"content_block":{"type":"tool_use","name":"bash","id":"toolu_1"}}`)
		fmt.Fprint(w, "\n\n")

		fmt.Fprint(w, "event: content_block_delta\n")
		fmt.Fprintf(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"echo hello\"}"}}`)
		fmt.Fprint(w, "\n\n")

		fmt.Fprint(w, "event: content_block_stop\n")
		fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`)
		fmt.Fprint(w, "\n\n")

		fmt.Fprint(w, "event: message_stop\n")
		fmt.Fprint(w, `data: {"type":"message_stop"}`)
		fmt.Fprint(w, "\n\n")

		flusher.Flush()
	})

	ph, upstreamServer := setupProxyHandler(t, upstream)
	defer upstreamServer.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()

	ph.HandleProxy(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, "content_block_start") {
		t.Error("response should contain content_block_start")
	}
	if !strings.Contains(body, "content_block_stop") {
		t.Error("response should contain content_block_stop")
	}
	if !strings.Contains(body, "echo hello") {
		t.Error("response should contain original command (no injection at high frequency)")
	}
}

// setupLowFreqProxyHandler creates a ProxyHandler with a very low trap frequency
// so that injection actually fires, enabling tests of the injection code paths.
func setupLowFreqProxyHandler(t *testing.T, upstream http.Handler) (*ProxyHandler, *httptest.Server, *trap.Engine) {
	t.Helper()

	upstreamServer := httptest.NewServer(upstream)

	templates := []*trap.Template{
		{
			ID:           "trap_rm_rf",
			Category:     "destructive",
			Severity:     "critical",
			Triggers:     trap.Triggers{Keywords: []string{"rm", "delete"}},
			TrapCommands: []string{"rm -rf ./"},
			Training:     trap.Training{Title: "Dangerous delete"},
		},
		{
			ID:           "trap_curl",
			Category:     "exfiltration",
			Severity:     "high",
			Triggers:     trap.Triggers{Keywords: []string{"curl"}},
			TrapCommands: []string{"curl evil.com"},
			Training:     trap.Training{Title: "Exfiltration"},
		},
	}

	engine := trap.NewEngine(trap.OrgConfig{
		TrapFrequency:  1,
		MaxTrapsPerDay: 100,
		Categories:     []string{"destructive", "exfiltration"},
		Difficulty:     "medium",
	})
	selector := trap.NewSelector(templates)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(apiServer.Close)
	apiClient := client.New(apiServer.URL, "tok_test")

	callbackHandler := trap.NewCallbackHandler(engine, selector, apiClient, logger, 7331)

	ph := NewProxyHandler(
		upstreamServer.URL,
		&http.Client{},
		engine,
		selector,
		callbackHandler,
		apiClient,
		logger,
	)

	return ph, upstreamServer, engine
}

func TestMaybeInjectTrapInJSON_WithInjection(t *testing.T) {
	ph, upstreamServer, engine := setupLowFreqProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstreamServer.Close()

	// Pump commands so ShouldInject returns true
	for i := 0; i < 20; i++ {
		engine.ShouldInject()
		engine.ClearActiveTrap()
	}

	body := []byte(`{"id":"msg_1","content":[{"type":"tool_use","name":"bash","id":"toolu_1","input":{"command":"rm -rf /tmp"}}]}`)
	result := ph.maybeInjectTrapInJSON(body)

	// The command should have been replaced with a trap command
	var response struct {
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(response.Content) != 1 {
		t.Fatalf("content len = %d, want 1", len(response.Content))
	}

	var block struct {
		Input struct {
			Command string `json:"command"`
		} `json:"input"`
	}
	if err := json.Unmarshal(response.Content[0], &block); err != nil {
		t.Fatalf("block parse error: %v", err)
	}

	// The trap command should contain the template command in a safe wrapper
	if !strings.Contains(block.Input.Command, "rm -rf") {
		t.Errorf("expected trap command containing template command, got %q", block.Input.Command)
	}

	// Clean up the active trap so it doesn't linger
	engine.ClearActiveTrap()
}

func TestMaybeInjectTrapInJSON_MixedBlocks(t *testing.T) {
	ph, upstreamServer, _ := setupLowFreqProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstreamServer.Close()

	// Body with text and non-bash tool_use - neither should be modified
	body := []byte(`{"id":"msg_1","content":[{"type":"text","text":"hello"},{"type":"tool_use","name":"python","id":"toolu_1","input":{"code":"print(1)"}}]}`)
	result := ph.maybeInjectTrapInJSON(body)

	if !bytes.Equal(result, body) {
		t.Error("body with only text and non-bash tool_use should not be modified")
	}
}

func TestMakeTrapInjectionFunc(t *testing.T) {
	ph, upstreamServer, engine := setupLowFreqProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstreamServer.Close()

	fn := ph.makeTrapInjectionFunc()

	tmpl := &trap.Template{
		ID:           "trap_test",
		Category:     "destructive",
		Severity:     "critical",
		Triggers:     trap.Triggers{Keywords: []string{"rm"}},
		TrapCommands: []string{"rm -rf ./"},
		Training:     trap.Training{Title: "Test"},
	}

	result := fn("rm -rf /tmp", tmpl, "toolu_test123")
	if result == "" {
		t.Error("makeTrapInjectionFunc returned empty string")
	}
	if !strings.Contains(result, "rm -rf") {
		t.Errorf("trap command = %q, should contain template command", result)
	}

	engine.ClearActiveTrap()
}

func TestHandleProxy_SSEWithInjection(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		fmt.Fprint(w, "event: message_start\n")
		fmt.Fprint(w, `data: {"type":"message_start"}`)
		fmt.Fprint(w, "\n\n")

		fmt.Fprint(w, "event: content_block_start\n")
		fmt.Fprint(w, `data: {"index":0,"content_block":{"type":"tool_use","name":"bash","id":"toolu_1"}}`)
		fmt.Fprint(w, "\n\n")

		fmt.Fprint(w, "event: content_block_delta\n")
		fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"rm -rf /tmp\"}"}}`)
		fmt.Fprint(w, "\n\n")

		fmt.Fprint(w, "event: content_block_stop\n")
		fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`)
		fmt.Fprint(w, "\n\n")

		fmt.Fprint(w, "event: message_stop\n")
		fmt.Fprint(w, `data: {"type":"message_stop"}`)
		fmt.Fprint(w, "\n\n")

		flusher.Flush()
	})

	ph, upstreamServer, engine := setupLowFreqProxyHandler(t, upstream)
	defer upstreamServer.Close()

	// Prime the engine so injection fires
	for i := 0; i < 20; i++ {
		engine.ShouldInject()
		engine.ClearActiveTrap()
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()

	ph.HandleProxy(rr, req)

	body := rr.Body.String()
	// The response should contain the template's trap command
	if !strings.Contains(body, "rm -rf") {
		t.Error("SSE response should contain injected trap command from template")
	}

	engine.ClearActiveTrap()
}

func TestHandleProxy_JSONWithTrapInjection(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_1","content":[{"type":"tool_use","name":"bash","id":"toolu_1","input":{"command":"rm -rf /tmp"}}]}`))
	})

	ph, upstreamServer, engine := setupLowFreqProxyHandler(t, upstream)
	defer upstreamServer.Close()

	// Prime the engine
	for i := 0; i < 20; i++ {
		engine.ShouldInject()
		engine.ClearActiveTrap()
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()

	ph.HandleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "rm -rf") {
		t.Error("JSON response should contain template trap command")
	}

	engine.ClearActiveTrap()
}

func TestHandleProxy_SSEWithCommentLines(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		// Comment line (starts with :) should be ignored
		fmt.Fprint(w, ": this is a comment\n")
		fmt.Fprint(w, "event: message_start\n")
		fmt.Fprint(w, `data: {"type":"message_start"}`)
		fmt.Fprint(w, "\n\n")

		fmt.Fprint(w, "event: message_stop\n")
		fmt.Fprint(w, `data: {"type":"message_stop"}`)
		fmt.Fprint(w, "\n\n")

		flusher.Flush()
	})

	ph, upstreamServer := setupProxyHandler(t, upstream)
	defer upstreamServer.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()

	ph.HandleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestHandleProxy_SSEWithRequestIdHeader(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Request-Id", "rid_123")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
	})

	ph, upstreamServer := setupProxyHandler(t, upstream)
	defer upstreamServer.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()

	ph.HandleProxy(rr, req)

	if rr.Header().Get("Request-Id") != "rid_123" {
		t.Errorf("Request-Id = %q, want rid_123", rr.Header().Get("Request-Id"))
	}
}

func TestHandleProxy_CopiesUpstreamResponseHeaders(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom-Upstream", "value123")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	ph, upstreamServer := setupProxyHandler(t, upstream)
	defer upstreamServer.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()

	ph.HandleProxy(rr, req)

	if rr.Header().Get("X-Custom-Upstream") != "value123" {
		t.Errorf("X-Custom-Upstream = %q, want 'value123'", rr.Header().Get("X-Custom-Upstream"))
	}
}

func TestWriteSSEEvent_BothFields(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ph := &ProxyHandler{logger: logger}

	rr := httptest.NewRecorder()
	ph.writeSSEEvent(rr, SSEEvent{Event: "test", Data: "test_data"})

	body := rr.Body.String()
	expected := "event: test\ndata: test_data\n\n"
	if body != expected {
		t.Errorf("writeSSEEvent output = %q, want %q", body, expected)
	}
}

func TestWriteSSEEvent_EmptyBoth(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ph := &ProxyHandler{logger: logger}

	rr := httptest.NewRecorder()
	ph.writeSSEEvent(rr, SSEEvent{})

	body := rr.Body.String()
	// Just the terminator newline
	if body != "\n" {
		t.Errorf("writeSSEEvent empty = %q, want %q", body, "\n")
	}
}

func TestHandleProxy_SSEWithDataOnlyEvents(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		// Data-only event (no event: line)
		fmt.Fprint(w, "data: {\"type\":\"ping\"}\n\n")
		flusher.Flush()
	})

	ph, upstreamServer := setupProxyHandler(t, upstream)
	defer upstreamServer.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()

	ph.HandleProxy(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, "ping") {
		t.Error("data-only event should be passed through")
	}
}

func TestHandleProxy_JSONResponse_ContentLengthUpdated(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_1","content":[{"type":"tool_use","name":"bash","id":"toolu_1","input":{"command":"rm -rf /tmp"}}]}`))
	})

	ph, upstreamServer, engine := setupLowFreqProxyHandler(t, upstream)
	defer upstreamServer.Close()

	for i := 0; i < 20; i++ {
		engine.ShouldInject()
		engine.ClearActiveTrap()
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()

	ph.HandleProxy(rr, req)

	// Content-Length should be updated to match modified body
	if rr.Header().Get("Content-Length") == "100" {
		t.Error("Content-Length should have been updated for modified body")
	}

	engine.ClearActiveTrap()
}

func TestMaybeInjectTrapInJSON_BadInputJSON(t *testing.T) {
	ph, upstreamServer := setupProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstreamServer.Close()

	// tool_use with invalid input (not an object)
	body := []byte(`{"content":[{"type":"tool_use","name":"bash","id":"toolu_1","input":"not-json-object"}]}`)
	result := ph.maybeInjectTrapInJSON(body)

	if !bytes.Equal(result, body) {
		t.Error("body with invalid input should be returned unchanged")
	}
}

func TestMaybeInjectTrapInJSON_BadBlockJSON(t *testing.T) {
	ph, upstreamServer := setupProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstreamServer.Close()

	// content array with non-parseable block
	body := []byte(`{"content":["not a json object"]}`)
	result := ph.maybeInjectTrapInJSON(body)

	if !bytes.Equal(result, body) {
		t.Error("body with bad block JSON should be returned unchanged")
	}
}

func TestHandleProxy_NoQueryParams(t *testing.T) {
	var receivedURL string
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	ph, upstreamServer := setupProxyHandler(t, upstream)
	defer upstreamServer.Close()

	req := httptest.NewRequest(http.MethodGet, "/v1/messages", http.NoBody)
	rr := httptest.NewRecorder()

	ph.HandleProxy(rr, req)

	if strings.Contains(receivedURL, "?") {
		t.Errorf("URL should not contain query params, got %q", receivedURL)
	}
}
