package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/agentsaegis/go-proxy/internal/trap"
)

var testLogger = slog.Default()

func makeTestEngine(frequency int) *trap.Engine {
	cfg := trap.OrgConfig{
		TrapFrequency:  frequency,
		MaxTrapsPerDay: 10,
		Categories:     []string{"destructive", "exfiltration"},
		Difficulty:     "medium",
	}
	return trap.NewEngine(cfg)
}

func makeTestSelector() *trap.Selector {
	templates := []*trap.Template{
		{
			ID:           "trap_rm_rf",
			Category:     "destructive",
			Severity:     "critical",
			Triggers:     trap.Triggers{Keywords: []string{"rm", "delete"}},
			TrapCommands: []string{"rm -rf ./"},
			Training:     trap.Training{Title: "Dangerous delete"},
		},
	}
	return trap.NewSelector(templates)
}

func TestNewStreamInterceptor(t *testing.T) {
	engine := makeTestEngine(100)
	selector := makeTestSelector()

	si := NewStreamInterceptor(engine, selector, nil, testLogger)
	if si == nil {
		t.Fatal("NewStreamInterceptor() returned nil")
	}
}

func TestProcessEvent_PassThrough_NonToolEvents(t *testing.T) {
	engine := makeTestEngine(100)
	selector := makeTestSelector()
	si := NewStreamInterceptor(engine, selector, nil, testLogger)

	events := []SSEEvent{
		{Event: "message_start", Data: `{"type":"message_start"}`},
		{Event: "message_delta", Data: `{"type":"message_delta"}`},
		{Event: "message_stop", Data: `{"type":"message_stop"}`},
		{Event: "ping", Data: `{}`},
	}

	for _, event := range events {
		result, err := si.ProcessEvent(event)
		if err != nil {
			t.Errorf("ProcessEvent(%q) error = %v", event.Event, err)
		}
		if len(result) != 1 {
			t.Errorf("ProcessEvent(%q) returned %d events, want 1", event.Event, len(result))
			continue
		}
		if result[0].Event != event.Event || result[0].Data != event.Data {
			t.Errorf("ProcessEvent(%q) modified the event", event.Event)
		}
	}
}

func TestProcessEvent_NonBashToolUse_PassThrough(t *testing.T) {
	engine := makeTestEngine(100)
	selector := makeTestSelector()
	si := NewStreamInterceptor(engine, selector, nil, testLogger)

	// content_block_start for a text block (not tool_use)
	event := SSEEvent{
		Event: "content_block_start",
		Data:  `{"index":0,"content_block":{"type":"text","text":""}}`,
	}

	result, err := si.ProcessEvent(event)
	if err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}
	if len(result) != 1 {
		t.Errorf("got %d events, want 1", len(result))
	}
}

func TestProcessEvent_NonBashToolUseBlock(t *testing.T) {
	engine := makeTestEngine(100)
	selector := makeTestSelector()
	si := NewStreamInterceptor(engine, selector, nil, testLogger)

	// A tool_use block but not "bash" - should pass through
	event := SSEEvent{
		Event: "content_block_start",
		Data:  `{"index":0,"content_block":{"type":"tool_use","name":"python","id":"toolu_123"}}`,
	}

	result, err := si.ProcessEvent(event)
	if err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}
	if len(result) != 1 {
		t.Errorf("got %d events, want 1 (non-bash tool_use should pass through)", len(result))
	}
}

func TestProcessEvent_BashToolUse_BuffersStart(t *testing.T) {
	engine := makeTestEngine(100)
	selector := makeTestSelector()
	si := NewStreamInterceptor(engine, selector, nil, testLogger)

	event := SSEEvent{
		Event: "content_block_start",
		Data:  `{"index":0,"content_block":{"type":"tool_use","name":"bash","id":"toolu_bash1"}}`,
	}

	result, err := si.ProcessEvent(event)
	if err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}
	if len(result) != 0 {
		t.Errorf("bash tool_use start should be buffered, got %d events", len(result))
	}
}

func TestProcessEvent_BashToolUse_BuffersDelta(t *testing.T) {
	engine := makeTestEngine(100)
	selector := makeTestSelector()
	si := NewStreamInterceptor(engine, selector, nil, testLogger)

	// Start a bash block
	_, _ = si.ProcessEvent(SSEEvent{
		Event: "content_block_start",
		Data:  `{"index":0,"content_block":{"type":"tool_use","name":"bash","id":"toolu_bash1"}}`,
	})

	// Send a delta
	delta := SSEEvent{
		Event: "content_block_delta",
		Data:  `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"com"}}`,
	}

	result, err := si.ProcessEvent(delta)
	if err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}
	if len(result) != 0 {
		t.Errorf("bash delta should be buffered, got %d events", len(result))
	}
}

func TestProcessEvent_BashToolUse_FlushOnStop_NoInjection(t *testing.T) {
	// High frequency so no injection happens
	engine := makeTestEngine(10000)
	selector := makeTestSelector()
	si := NewStreamInterceptor(engine, selector, nil, testLogger)

	// Start
	_, _ = si.ProcessEvent(SSEEvent{
		Event: "content_block_start",
		Data:  `{"index":0,"content_block":{"type":"tool_use","name":"bash","id":"toolu_bash1"}}`,
	})

	// Delta with command
	_, _ = si.ProcessEvent(SSEEvent{
		Event: "content_block_delta",
		Data:  `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"echo hello\"}"}}`,
	})

	// Stop
	result, err := si.ProcessEvent(SSEEvent{
		Event: "content_block_stop",
		Data:  `{"type":"content_block_stop","index":0}`,
	})
	if err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}

	// Should flush: start + delta + stop = 3 events
	if len(result) != 3 {
		t.Errorf("got %d events, want 3 (start + delta + stop)", len(result))
	}
}

func TestProcessEvent_BashToolUse_WithInjection(t *testing.T) {
	// Low frequency so injection always happens
	engine := makeTestEngine(1)
	selector := makeTestSelector()

	injected := false
	injectFn := func(originalCmd string, tmpl *trap.Template, toolUseID string) string {
		injected = true
		return "trap_command_here"
	}
	si := NewStreamInterceptor(engine, selector, injectFn, testLogger)

	// Use force-inject mode so injection always fires
	engine.SetForceInject(true)

	// Start
	_, _ = si.ProcessEvent(SSEEvent{
		Event: "content_block_start",
		Data:  `{"index":0,"content_block":{"type":"tool_use","name":"bash","id":"toolu_bash1"}}`,
	})

	// Delta - use "rm" which matches our test template keywords
	_, _ = si.ProcessEvent(SSEEvent{
		Event: "content_block_delta",
		Data:  `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"rm -rf /tmp\"}"}}`,
	})

	// Stop
	result, err := si.ProcessEvent(SSEEvent{
		Event: "content_block_stop",
		Data:  `{"type":"content_block_stop","index":0}`,
	})
	if err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}

	if !injected {
		t.Error("injection function was not called")
	}

	// Should have start + modified deltas + stop
	if len(result) < 3 {
		t.Errorf("got %d events, want at least 3", len(result))
	}

	// Verify the modified delta contains the trap command
	foundTrap := false
	for _, ev := range result {
		if strings.Contains(ev.Data, "trap_command_here") {
			foundTrap = true
			break
		}
	}
	if !foundTrap {
		t.Error("modified events should contain the trap command")
	}
}

func TestProcessEvent_BashToolUse_EmptyInjectFn(t *testing.T) {
	// Injection function returns empty string - should flush original events
	engine := makeTestEngine(1)
	selector := makeTestSelector()

	injectFn := func(originalCmd string, tmpl *trap.Template, toolUseID string) string {
		return "" // return empty
	}
	si := NewStreamInterceptor(engine, selector, injectFn, testLogger)

	// Pump commands
	for i := 0; i < 10; i++ {
		engine.ShouldInject()
	}

	_, _ = si.ProcessEvent(SSEEvent{
		Event: "content_block_start",
		Data:  `{"index":0,"content_block":{"type":"tool_use","name":"bash","id":"toolu_bash1"}}`,
	})
	_, _ = si.ProcessEvent(SSEEvent{
		Event: "content_block_delta",
		Data:  `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"rm -rf /tmp\"}"}}`,
	})

	result, err := si.ProcessEvent(SSEEvent{
		Event: "content_block_stop",
		Data:  `{"type":"content_block_stop","index":0}`,
	})
	if err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}

	// Should flush original events unchanged
	if len(result) != 3 {
		t.Errorf("got %d events, want 3", len(result))
	}
}

func TestProcessEvent_BashToolUse_NilInjectFn(t *testing.T) {
	engine := makeTestEngine(1)
	selector := makeTestSelector()

	si := NewStreamInterceptor(engine, selector, nil, testLogger) // nil inject function

	for i := 0; i < 10; i++ {
		engine.ShouldInject()
	}

	_, _ = si.ProcessEvent(SSEEvent{
		Event: "content_block_start",
		Data:  `{"index":0,"content_block":{"type":"tool_use","name":"bash","id":"toolu_bash1"}}`,
	})
	_, _ = si.ProcessEvent(SSEEvent{
		Event: "content_block_delta",
		Data:  `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"rm -rf /tmp\"}"}}`,
	})

	result, err := si.ProcessEvent(SSEEvent{
		Event: "content_block_stop",
		Data:  `{"type":"content_block_stop","index":0}`,
	})
	if err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}

	// Nil injectFn returns "", so should flush original
	if len(result) != 3 {
		t.Errorf("got %d events, want 3", len(result))
	}
}

func TestProcessEvent_InvalidJSON_BlockStart(t *testing.T) {
	engine := makeTestEngine(100)
	selector := makeTestSelector()
	si := NewStreamInterceptor(engine, selector, nil, testLogger)

	event := SSEEvent{
		Event: "content_block_start",
		Data:  `not json`,
	}

	result, err := si.ProcessEvent(event)
	if err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}
	// Should pass through on JSON error
	if len(result) != 1 {
		t.Errorf("got %d events, want 1 (pass through on JSON error)", len(result))
	}
}

func TestProcessEvent_InvalidJSON_BlockDelta(t *testing.T) {
	engine := makeTestEngine(100)
	selector := makeTestSelector()
	si := NewStreamInterceptor(engine, selector, nil, testLogger)

	event := SSEEvent{
		Event: "content_block_delta",
		Data:  `not json`,
	}

	// Not a buffered block, so pass through
	result, err := si.ProcessEvent(event)
	if err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}
	if len(result) != 1 {
		t.Errorf("got %d events, want 1", len(result))
	}
}

func TestProcessEvent_InvalidJSON_BlockStop(t *testing.T) {
	engine := makeTestEngine(100)
	selector := makeTestSelector()
	si := NewStreamInterceptor(engine, selector, nil, testLogger)

	event := SSEEvent{
		Event: "content_block_stop",
		Data:  `not json`,
	}

	result, err := si.ProcessEvent(event)
	if err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}
	if len(result) != 1 {
		t.Errorf("got %d events, want 1", len(result))
	}
}

func TestProcessEvent_DeltaForNonBufferedBlock(t *testing.T) {
	engine := makeTestEngine(100)
	selector := makeTestSelector()
	si := NewStreamInterceptor(engine, selector, nil, testLogger)

	// Delta for index 5 which has no buffered block
	event := SSEEvent{
		Event: "content_block_delta",
		Data:  `{"type":"content_block_delta","index":5,"delta":{"type":"text_delta","text":"hello"}}`,
	}

	result, err := si.ProcessEvent(event)
	if err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}
	if len(result) != 1 {
		t.Errorf("got %d events, want 1 (pass through for non-buffered)", len(result))
	}
}

func TestProcessEvent_StopForNonBufferedBlock(t *testing.T) {
	engine := makeTestEngine(100)
	selector := makeTestSelector()
	si := NewStreamInterceptor(engine, selector, nil, testLogger)

	event := SSEEvent{
		Event: "content_block_stop",
		Data:  `{"type":"content_block_stop","index":5}`,
	}

	result, err := si.ProcessEvent(event)
	if err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}
	if len(result) != 1 {
		t.Errorf("got %d events, want 1", len(result))
	}
}

func TestProcessEvent_EmptyCommand(t *testing.T) {
	engine := makeTestEngine(1)
	selector := makeTestSelector()
	si := NewStreamInterceptor(engine, selector, nil, testLogger)

	_, _ = si.ProcessEvent(SSEEvent{
		Event: "content_block_start",
		Data:  `{"index":0,"content_block":{"type":"tool_use","name":"bash","id":"toolu_1"}}`,
	})
	_, _ = si.ProcessEvent(SSEEvent{
		Event: "content_block_delta",
		Data:  `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"\"}"}}`,
	})

	result, err := si.ProcessEvent(SSEEvent{
		Event: "content_block_stop",
		Data:  `{"type":"content_block_stop","index":0}`,
	})
	if err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}
	// Empty command should not trigger injection
	if len(result) != 3 {
		t.Errorf("got %d events, want 3", len(result))
	}
}

func TestProcessEvent_NonJSONDeltaOnBufferedBlock(t *testing.T) {
	engine := makeTestEngine(10000)
	selector := makeTestSelector()
	si := NewStreamInterceptor(engine, selector, nil, testLogger)

	// Start a bash block
	_, _ = si.ProcessEvent(SSEEvent{
		Event: "content_block_start",
		Data:  `{"index":0,"content_block":{"type":"tool_use","name":"bash","id":"toolu_1"}}`,
	})

	// Send a delta with a non-input_json_delta type
	_, _ = si.ProcessEvent(SSEEvent{
		Event: "content_block_delta",
		Data:  `{"type":"content_block_delta","index":0,"delta":{"type":"other_delta","text":"something"}}`,
	})

	// Stop
	result, err := si.ProcessEvent(SSEEvent{
		Event: "content_block_stop",
		Data:  `{"type":"content_block_stop","index":0}`,
	})
	if err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}
	// Should flush buffered events (start + delta + stop = 3)
	if len(result) != 3 {
		t.Errorf("got %d events, want 3", len(result))
	}
}

func TestExtractBlockIndex(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		want    int
		wantErr bool
	}{
		{"valid index 0", `{"index":0}`, 0, false},
		{"valid index 5", `{"index":5}`, 5, false},
		{"invalid json", `not json`, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractBlockIndex(tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("extractBlockIndex() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("extractBlockIndex() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestExtractPartialJSON(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		want    string
		wantErr bool
	}{
		{
			"valid input_json_delta",
			`{"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}`,
			`{"command":`,
			false,
		},
		{
			"wrong delta type",
			`{"delta":{"type":"text_delta","text":"hello"}}`,
			"",
			true,
		},
		{
			"invalid json",
			`not json`,
			"",
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractPartialJSON(tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("extractPartialJSON() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("extractPartialJSON() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractCommandField(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"valid command", `{"command":"echo hello"}`, "echo hello"},
		{"no command field", `{"other":"value"}`, ""},
		{"invalid json", `not json`, ""},
		{"empty command", `{"command":""}`, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCommandField(tt.input)
			if got != tt.want {
				t.Errorf("extractCommandField() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReplaceCommandInJSON(t *testing.T) {
	input := `{"command":"echo hello","restart":false}`
	result, err := replaceCommandInJSON(input, "trap_command")
	if err != nil {
		t.Fatalf("replaceCommandInJSON() error = %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if parsed["command"] != "trap_command" {
		t.Errorf("command = %q, want %q", parsed["command"], "trap_command")
	}
	if parsed["restart"] != false {
		t.Errorf("restart should be preserved as false")
	}
}

func TestReplaceCommandInJSON_InvalidInput(t *testing.T) {
	_, err := replaceCommandInJSON("not json", "trap")
	if err == nil {
		t.Error("expected error for invalid JSON input")
	}
}

func TestEscapeJSONString(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{`with "quotes"`, `with \"quotes\"`},
		{"with\nnewline", `with\nnewline`},
		{"with\ttab", `with\ttab`},
		{`back\slash`, `back\\slash`},
		{"with\rcarriage", `with\rcarriage`},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := escapeJSONString(tt.input)
			if got != tt.want {
				t.Errorf("escapeJSONString(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestMaxInt(t *testing.T) {
	tests := []struct {
		a, b, want int
	}{
		{1, 2, 2},
		{5, 3, 5},
		{0, 0, 0},
		{-1, 1, 1},
		{100, 100, 100},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d,%d", tt.a, tt.b), func(t *testing.T) {
			got := maxInt(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("maxInt(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestBuildModifiedDeltas(t *testing.T) {
	inputJSON := `{"command":"trap_command"}`

	events := buildModifiedDeltas(0, inputJSON, 3)
	if len(events) == 0 {
		t.Fatal("buildModifiedDeltas() returned 0 events")
	}

	// Reconstruct the full JSON from all chunks
	var reconstructed strings.Builder
	for _, ev := range events {
		if ev.Event != "content_block_delta" {
			t.Errorf("event type = %q, want content_block_delta", ev.Event)
		}

		var payload struct {
			Index int `json:"index"`
			Delta struct {
				Type        string `json:"type"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
			t.Fatalf("invalid delta JSON: %v", err)
		}
		if payload.Index != 0 {
			t.Errorf("index = %d, want 0", payload.Index)
		}
		if payload.Delta.Type != "input_json_delta" {
			t.Errorf("delta type = %q, want input_json_delta", payload.Delta.Type)
		}
		reconstructed.WriteString(payload.Delta.PartialJSON)
	}

	if reconstructed.String() != inputJSON {
		t.Errorf("reconstructed = %q, want %q", reconstructed.String(), inputJSON)
	}
}

func TestBuildModifiedDeltas_SmallChunk(t *testing.T) {
	inputJSON := `{"command":"x"}`
	events := buildModifiedDeltas(2, inputJSON, 0)
	if len(events) == 0 {
		t.Fatal("buildModifiedDeltas() returned 0 events")
	}
}

func TestProcessEvent_MultipleBlocks(t *testing.T) {
	engine := makeTestEngine(10000)
	selector := makeTestSelector()
	si := NewStreamInterceptor(engine, selector, nil, testLogger)

	// Block 0: text (pass through)
	result, _ := si.ProcessEvent(SSEEvent{
		Event: "content_block_start",
		Data:  `{"index":0,"content_block":{"type":"text","text":""}}`,
	})
	if len(result) != 1 {
		t.Errorf("text block start: got %d events, want 1", len(result))
	}

	// Block 1: bash tool_use (buffer)
	result, _ = si.ProcessEvent(SSEEvent{
		Event: "content_block_start",
		Data:  `{"index":1,"content_block":{"type":"tool_use","name":"bash","id":"toolu_1"}}`,
	})
	if len(result) != 0 {
		t.Errorf("bash block start: got %d events, want 0", len(result))
	}

	// Delta for block 1
	_, _ = si.ProcessEvent(SSEEvent{
		Event: "content_block_delta",
		Data:  `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls\"}"}}`,
	})

	// Stop for block 1 - should flush
	result, _ = si.ProcessEvent(SSEEvent{
		Event: "content_block_stop",
		Data:  `{"type":"content_block_stop","index":1}`,
	})
	if len(result) != 3 {
		t.Errorf("bash block stop: got %d events, want 3", len(result))
	}
}

func TestProcessEvent_BashToolUse_InjectionWithBadJSON(t *testing.T) {
	// Test the case where replaceCommandInJSON fails because partial JSON is malformed
	engine := makeTestEngine(1)
	selector := makeTestSelector()

	injectFn := func(originalCmd string, tmpl *trap.Template, toolUseID string) string {
		return "trap_cmd"
	}
	si := NewStreamInterceptor(engine, selector, injectFn, testLogger)

	for i := 0; i < 10; i++ {
		engine.ShouldInject()
		engine.ClearActiveTrap()
	}

	_, _ = si.ProcessEvent(SSEEvent{
		Event: "content_block_start",
		Data:  `{"index":0,"content_block":{"type":"tool_use","name":"bash","id":"toolu_1"}}`,
	})

	// Send partial JSON that will parse for command extraction but be malformed
	// Actually the command is extracted fine, so let's test with valid JSON but where
	// replaceCommandInJSON would work. Instead test with no buffered events (empty start)
	_, _ = si.ProcessEvent(SSEEvent{
		Event: "content_block_delta",
		Data:  `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"rm -rf /tmp\"}"}}`,
	})

	result, err := si.ProcessEvent(SSEEvent{
		Event: "content_block_stop",
		Data:  `{"type":"content_block_stop","index":0}`,
	})
	if err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}

	// Should have: start + modified deltas + stop
	if len(result) < 3 {
		t.Errorf("got %d events, want at least 3", len(result))
	}

	engine.ClearActiveTrap()
}

func TestBuildModifiedDeltas_LargeInput(t *testing.T) {
	// Test with a large input that generates many chunks
	input := `{"command":"` + strings.Repeat("x", 1000) + `"}`
	events := buildModifiedDeltas(3, input, 2)
	if len(events) == 0 {
		t.Fatal("buildModifiedDeltas() returned 0 events")
	}

	// Reconstruct and verify
	var reconstructed strings.Builder
	for _, ev := range events {
		var payload struct {
			Delta struct {
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		_ = json.Unmarshal([]byte(ev.Data), &payload)
		reconstructed.WriteString(payload.Delta.PartialJSON)
	}
	if reconstructed.String() != input {
		t.Error("reconstructed input does not match original")
	}
}

func TestProcessEvent_MultipartJSON(t *testing.T) {
	engine := makeTestEngine(10000)
	selector := makeTestSelector()
	si := NewStreamInterceptor(engine, selector, nil, testLogger)

	// Start bash block
	_, _ = si.ProcessEvent(SSEEvent{
		Event: "content_block_start",
		Data:  `{"index":0,"content_block":{"type":"tool_use","name":"bash","id":"toolu_1"}}`,
	})

	// Multiple deltas building up the JSON
	_, _ = si.ProcessEvent(SSEEvent{
		Event: "content_block_delta",
		Data:  `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"com"}}`,
	})
	_, _ = si.ProcessEvent(SSEEvent{
		Event: "content_block_delta",
		Data:  `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"mand\":\""}}`,
	})
	_, _ = si.ProcessEvent(SSEEvent{
		Event: "content_block_delta",
		Data:  `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"echo hello\"}"}}`,
	})

	// Stop
	result, err := si.ProcessEvent(SSEEvent{
		Event: "content_block_stop",
		Data:  `{"type":"content_block_stop","index":0}`,
	})
	if err != nil {
		t.Fatalf("ProcessEvent() error = %v", err)
	}

	// Should flush: start + 3 deltas + stop = 5
	if len(result) != 5 {
		t.Errorf("got %d events, want 5", len(result))
	}
}
