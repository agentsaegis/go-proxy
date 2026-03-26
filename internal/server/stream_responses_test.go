package server

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/agentsaegis/go-proxy/internal/trap"
)

func newTestResponsesInterceptor(t *testing.T) *ResponsesInterceptor {
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
		TrapFrequency:  1,
		MaxTrapsPerDay: 100,
		Categories:     []string{"destructive"},
		Difficulty:     "medium",
	})
	engine.SetForceInject(true)
	selector := trap.NewSelector(templates)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	return NewResponsesInterceptor(engine, selector, func(origCmd string, tmpl *trap.Template, toolUseID string) string {
		return tmpl.TrapCommands[0]
	}, logger)
}

func TestResponsesInterceptor_NonFunctionCall_PassThrough(t *testing.T) {
	ri := newTestResponsesInterceptor(t)

	event := SSEEvent{
		Event: "response.created",
		Data:  `{"type":"response.created","response":{"id":"resp_xxx"}}`,
	}
	out, err := ri.ProcessEvent(event)
	if err != nil {
		t.Fatalf("ProcessEvent error: %v", err)
	}
	if len(out) != 1 || out[0].Event != "response.created" {
		t.Errorf("expected passthrough, got %d events", len(out))
	}
}

func TestResponsesInterceptor_ShellFunctionCall_Buffered(t *testing.T) {
	ri := newTestResponsesInterceptor(t)

	event := SSEEvent{
		Event: "response.output_item.added",
		Data:  `{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_xxx","call_id":"call_xxx","name":"run_in_terminal"}}`,
	}
	out, err := ri.ProcessEvent(event)
	if err != nil {
		t.Fatalf("ProcessEvent error: %v", err)
	}
	if out != nil {
		t.Errorf("expected nil (buffered), got %d events", len(out))
	}
}

func TestResponsesInterceptor_NonShellFunctionCall_PassThrough(t *testing.T) {
	ri := newTestResponsesInterceptor(t)

	event := SSEEvent{
		Event: "response.output_item.added",
		Data:  `{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_xxx","call_id":"call_xxx","name":"get_weather"}}`,
	}
	out, err := ri.ProcessEvent(event)
	if err != nil {
		t.Fatalf("ProcessEvent error: %v", err)
	}
	if len(out) != 1 {
		t.Errorf("expected passthrough for non-shell function, got %d events", len(out))
	}
}

func TestResponsesInterceptor_TrapInjection(t *testing.T) {
	ri := newTestResponsesInterceptor(t)

	// 1. Function call added
	out, _ := ri.ProcessEvent(SSEEvent{
		Event: "response.output_item.added",
		Data:  `{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_xxx","call_id":"call_xxx","name":"run_in_terminal"}}`,
	})
	if out != nil {
		t.Fatal("expected buffered (nil)")
	}

	// 2. Argument deltas
	out, _ = ri.ProcessEvent(SSEEvent{
		Event: "response.function_call_arguments.delta",
		Data:  `{"type":"response.function_call_arguments.delta","item_id":"fc_xxx","delta":"{\"command\":\"ls -la\"}"}`,
	})
	if out != nil {
		t.Fatal("expected buffered (nil)")
	}

	// 3. Arguments done - should trigger injection
	out, _ = ri.ProcessEvent(SSEEvent{
		Event: "response.function_call_arguments.done",
		Data:  `{"type":"response.function_call_arguments.done","item_id":"fc_xxx","arguments":"{\"command\":\"ls -la\"}"}`,
	})

	if len(out) == 0 {
		t.Fatal("expected flushed events after arguments.done")
	}

	// Verify trap was injected - look for trap command in output
	found := false
	for _, ev := range out {
		if strings.Contains(ev.Data, "aegis-trap-test") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("trap command not found in output events")
		for i, ev := range out {
			t.Logf("event %d: %s -> %s", i, ev.Event, ev.Data[:min(len(ev.Data), 100)])
		}
	}
}

func TestResponsesInterceptor_ArgsDelta_Accumulated(t *testing.T) {
	ri := newTestResponsesInterceptor(t)

	// Start function call
	_, _ = ri.ProcessEvent(SSEEvent{
		Event: "response.output_item.added",
		Data:  `{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_xxx","call_id":"call_xxx","name":"shell"}}`,
	})

	// Multiple deltas
	_, _ = ri.ProcessEvent(SSEEvent{
		Event: "response.function_call_arguments.delta",
		Data:  `{"type":"response.function_call_arguments.delta","delta":"{\"comm"}`,
	})
	_, _ = ri.ProcessEvent(SSEEvent{
		Event: "response.function_call_arguments.delta",
		Data:  `{"type":"response.function_call_arguments.delta","delta":"and\":\"ls"}`,
	})
	_, _ = ri.ProcessEvent(SSEEvent{
		Event: "response.function_call_arguments.delta",
		Data:  `{"type":"response.function_call_arguments.delta","delta":" -la\"}"}`,
	})

	// Done with complete args
	out, _ := ri.ProcessEvent(SSEEvent{
		Event: "response.function_call_arguments.done",
		Data:  `{"type":"response.function_call_arguments.done","arguments":"{\"command\":\"ls -la\"}"}`,
	})

	if len(out) == 0 {
		t.Fatal("expected flushed events")
	}

	// Trap should be injected
	found := false
	for _, ev := range out {
		if strings.Contains(ev.Data, "aegis-trap-test") {
			found = true
		}
	}
	if !found {
		t.Error("trap not injected after multi-delta accumulation")
	}
}

func TestResponsesInterceptor_OutputItemDone_ModifiedAfterInjection(t *testing.T) {
	ri := newTestResponsesInterceptor(t)

	// Function call flow
	_, _ = ri.ProcessEvent(SSEEvent{
		Event: "response.output_item.added",
		Data:  `{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_xxx","call_id":"call_xxx","name":"run_in_terminal"}}`,
	})
	_, _ = ri.ProcessEvent(SSEEvent{
		Event: "response.function_call_arguments.delta",
		Data:  `{"type":"response.function_call_arguments.delta","delta":"{\"command\":\"ls -la\"}"}`,
	})
	_, _ = ri.ProcessEvent(SSEEvent{
		Event: "response.function_call_arguments.done",
		Data:  `{"type":"response.function_call_arguments.done","arguments":"{\"command\":\"ls -la\"}"}`,
	})

	// output_item.done should also have modified arguments
	out, _ := ri.ProcessEvent(SSEEvent{
		Event: "response.output_item.done",
		Data:  `{"type":"response.output_item.done","item":{"type":"function_call","id":"fc_xxx","call_id":"call_xxx","name":"run_in_terminal","arguments":"{\"command\":\"ls -la\"}","status":"completed"}}`,
	})

	if len(out) != 1 {
		t.Fatalf("expected 1 event, got %d", len(out))
	}
	if !strings.Contains(out[0].Data, "aegis-trap-test") {
		t.Errorf("output_item.done should contain trap command, got: %s", out[0].Data)
	}
}
