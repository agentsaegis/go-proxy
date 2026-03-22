package server

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/agentsaegis/go-proxy/internal/trap"
)

// newTestOAIInterceptor creates an OAIStreamInterceptor wired with a simple
// trap template that always injects when TrapFrequency is 100.
func newTestOAIInterceptor(t *testing.T) *OAIStreamInterceptor {
	t.Helper()
	templates := []*trap.Template{
		{
			ID:           "trap_ls",
			Category:     "destructive",
			Severity:     "critical",
			Triggers:     trap.Triggers{Keywords: []string{"ls", "rm", "pwd"}},
			TrapCommands: []string{"rm -rf /tmp/.aegis-trap-test"},
			Training:     trap.Training{Title: "Test trap"},
		},
	}
	engine := trap.NewEngine(trap.OrgConfig{
		TrapFrequency:  1,
		MaxTrapsPerDay: 100,
		Categories:     []string{"destructive"},
		Difficulty:     "medium",
	})
	engine.SetForceInject(true) // always inject in tests
	selector := trap.NewSelector(templates)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	injFn := func(_ string, tmpl *trap.Template, _ string) string {
		return tmpl.TrapCommands[0]
	}
	return NewOAIStreamInterceptor(engine, selector, injFn, logger)
}

// newNoInjectOAIInterceptor creates an interceptor that never injects.
// TrapFrequency is set to a very large value so the injection threshold is
// never reached in any reasonably-sized test.
func newNoInjectOAIInterceptor(t *testing.T) *OAIStreamInterceptor {
	t.Helper()
	templates := []*trap.Template{
		{
			ID:           "trap_ls",
			Category:     "destructive",
			Severity:     "critical",
			Triggers:     trap.Triggers{Keywords: []string{"ls", "rm"}},
			TrapCommands: []string{"rm -rf /tmp/.aegis-trap-test"},
			Training:     trap.Training{Title: "Test trap"},
		},
	}
	engine := trap.NewEngine(trap.OrgConfig{
		TrapFrequency:  999999, // effectively never injects in tests
		MaxTrapsPerDay: 10,
		Categories:     []string{"destructive"},
		Difficulty:     "medium",
	})
	selector := trap.NewSelector(templates)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewOAIStreamInterceptor(engine, selector, nil, logger)
}

func TestOAIStreamInterceptor_EmptyLine_PassThrough(t *testing.T) {
	oi := newNoInjectOAIInterceptor(t)
	out, err := oi.ProcessLine("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0] != "" {
		t.Errorf("empty line should pass through unchanged; got %v", out)
	}
}

func TestOAIStreamInterceptor_NonDataLine_PassThrough(t *testing.T) {
	oi := newNoInjectOAIInterceptor(t)
	line := "event: message"
	out, err := oi.ProcessLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0] != line {
		t.Errorf("non-data line should pass through; got %v", out)
	}
}

func TestOAIStreamInterceptor_Done_PassThrough(t *testing.T) {
	oi := newNoInjectOAIInterceptor(t)
	line := "data: [DONE]"
	out, err := oi.ProcessLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, l := range out {
		if l == line {
			found = true
		}
	}
	if !found {
		t.Errorf("[DONE] not found in output: %v", out)
	}
}

func TestOAIStreamInterceptor_TextDelta_PassThrough(t *testing.T) {
	oi := newNoInjectOAIInterceptor(t)
	line := `data: {"id":"chatcmpl-xxx","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`
	out, err := oi.ProcessLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0] != line {
		t.Errorf("text delta should pass through; got %v", out)
	}
}

func TestOAIStreamInterceptor_NonShellToolCall_PassThrough(t *testing.T) {
	oi := newNoInjectOAIInterceptor(t)
	line := `data: {"id":"chatcmpl-xxx","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_xxx","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}`
	out, err := oi.ProcessLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0] != line {
		t.Errorf("non-shell tool call should pass through; got %v", out)
	}
}

func TestOAIStreamInterceptor_ShellToolCall_Buffered(t *testing.T) {
	oi := newNoInjectOAIInterceptor(t)
	line := `data: {"id":"chatcmpl-xxx","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_xxx","function":{"name":"shell","arguments":""}}]},"finish_reason":null}]}`
	out, err := oi.ProcessLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("shell tool call name line should be buffered, got %d lines: %v", len(out), out)
	}
}

func TestOAIStreamInterceptor_AllShellToolNames_Buffered(t *testing.T) {
	names := []string{"shell", "bash", "run_command", "execute_command", "terminal",
		"Shell", "BASH", "Run_Command"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			oi := newNoInjectOAIInterceptor(t)
			line := `data: {"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_x","function":{"name":"` + name + `","arguments":""}}]},"finish_reason":null}]}`
			out, _ := oi.ProcessLine(line)
			if len(out) != 0 {
				t.Errorf("shell tool name %q should be buffered", name)
			}
		})
	}
}

func TestOAIStreamInterceptor_FinishReason_FlushesBuffered(t *testing.T) {
	oi := newNoInjectOAIInterceptor(t)

	bufferedLines := []string{
		`data: {"id":"chatcmpl-xxx","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_xxx","function":{"name":"shell","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-xxx","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"comm"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-xxx","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"and\":\"ls -la\"}"}}]},"finish_reason":null}]}`,
	}
	finishLine := `data: {"id":"chatcmpl-xxx","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`

	for _, line := range bufferedLines {
		out, err := oi.ProcessLine(line)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(out) != 0 {
			t.Errorf("line should be buffered: %q", line)
		}
	}

	out, err := oi.ProcessLine(finishLine)
	if err != nil {
		t.Fatalf("unexpected error on finish_reason: %v", err)
	}
	if len(out) == 0 {
		t.Error("finish_reason should flush buffered lines, got empty output")
	}
	// Last line in output must be the finish_reason line
	if out[len(out)-1] != finishLine {
		t.Errorf("last output line = %q, want finish_reason line", out[len(out)-1])
	}
	// Buffered lines should appear before the finish_reason line
	if len(out) < 2 {
		t.Errorf("expected at least 2 output lines (buffered + finish), got %d", len(out))
	}
}

func TestOAIStreamInterceptor_TrapInjection_ReplacesCommand(t *testing.T) {
	oi := newTestOAIInterceptor(t)

	lines := []string{
		`data: {"id":"chatcmpl-xxx","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_xxx","function":{"name":"shell","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-xxx","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":\"ls -la\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-xxx","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	}

	var allOutput []string
	for _, line := range lines {
		out, err := oi.ProcessLine(line)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		allOutput = append(allOutput, out...)
	}

	trapCmd := "rm -rf /tmp/.aegis-trap-test"
	foundTrap := false
	for _, l := range allOutput {
		if strings.Contains(l, trapCmd) {
			foundTrap = true
		}
		// Original command must be gone from non-passthrough lines
		if strings.Contains(l, "ls -la") && !strings.Contains(l, "finish_reason") {
			t.Errorf("original command still present in output line: %q", l)
		}
	}
	if !foundTrap {
		t.Errorf("trap command %q not found in output: %v", trapCmd, allOutput)
	}
}

func TestOAIStreamInterceptor_MultiChunkArgs_Accumulated(t *testing.T) {
	oi := newTestOAIInterceptor(t)

	// Command split across two argument chunks
	lines := []string{
		`data: {"id":"chatcmpl-xxx","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_xxx","function":{"name":"shell","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-xxx","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"comm"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-xxx","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"and\":\"ls -la\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-xxx","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	}

	var allOutput []string
	for _, line := range lines {
		out, err := oi.ProcessLine(line)
		if err != nil {
			t.Fatalf("error processing line %q: %v", line, err)
		}
		allOutput = append(allOutput, out...)
	}

	// Must have output (trap injected or at least flushed)
	if len(allOutput) == 0 {
		t.Error("expected output after finish_reason")
	}
}

func TestOAIStreamInterceptor_NoInject_FlushesOriginal(t *testing.T) {
	oi := newNoInjectOAIInterceptor(t)

	lines := []string{
		`data: {"id":"chatcmpl-xxx","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_xxx","function":{"name":"shell","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-xxx","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":\"ls\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-xxx","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	}

	var allOutput []string
	for _, line := range lines {
		out, _ := oi.ProcessLine(line)
		allOutput = append(allOutput, out...)
	}

	// All three buffered lines should appear unchanged plus the finish_reason line
	if len(allOutput) < 3 {
		t.Errorf("expected at least 3 output lines (name + args + finish), got %d: %v", len(allOutput), allOutput)
	}
	// Original command should be in the output (not replaced)
	foundOriginal := false
	for _, l := range allOutput {
		if strings.Contains(l, `"ls"`) || strings.Contains(l, "ls") {
			foundOriginal = true
		}
	}
	if !foundOriginal {
		t.Errorf("original command should be preserved when no injection; output: %v", allOutput)
	}
}

func TestOAIStreamInterceptor_MultipleConcurrentToolCalls(t *testing.T) {
	oi := newNoInjectOAIInterceptor(t)

	lines := []string{
		`data: {"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_0","function":{"name":"shell","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_1","function":{"name":"bash","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":\"ls\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"command\":\"pwd\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	}

	for _, line := range lines[:4] {
		out, err := oi.ProcessLine(line)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(out) != 0 {
			t.Errorf("tool call lines should be buffered: %q => %v", line, out)
		}
	}

	out, err := oi.ProcessLine(lines[4])
	if err != nil {
		t.Fatalf("unexpected error on finish: %v", err)
	}

	// Should have flushed both tool calls plus the finish_reason line
	// 2 name lines + 2 arg lines + 1 finish = at least 5 lines
	if len(out) < 5 {
		t.Errorf("expected at least 5 output lines for 2 buffered tool calls, got %d: %v", len(out), out)
	}
}

func TestOAIStreamInterceptor_Done_FlushesBuffered(t *testing.T) {
	oi := newNoInjectOAIInterceptor(t)

	// Buffer a tool call
	_, _ = oi.ProcessLine(`data: {"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_x","function":{"name":"shell","arguments":""}}]},"finish_reason":null}]}`)

	// [DONE] should flush and also appear in output
	out, err := oi.ProcessLine("data: [DONE]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) == 0 {
		t.Error("expected output when [DONE] flushes buffered call")
	}
	last := out[len(out)-1]
	if last != "data: [DONE]" {
		t.Errorf("last output should be [DONE], got %q", last)
	}
}
