package server

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/agentsaegis/go-proxy/internal/trap"
)

// oaiChunk is a single OpenAI chat-completion streaming chunk.
type oaiChunk struct {
	ID      string      `json:"id"`
	Choices []oaiChoice `json:"choices"`
}

type oaiChoice struct {
	Index        int      `json:"index"`
	Delta        oaiDelta `json:"delta"`
	FinishReason *string  `json:"finish_reason"`
}

type oaiDelta struct {
	ToolCalls []oaiToolCallDelta `json:"tool_calls"`
}

type oaiToolCallDelta struct {
	Index    int          `json:"index"`
	ID       string       `json:"id"`
	Function oaiFuncDelta `json:"function"`
}

type oaiFuncDelta struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// OAIToolCallState tracks a single tool call being assembled from SSE chunks.
type OAIToolCallState struct {
	Index           int
	FunctionName    string
	ArgumentsBuffer strings.Builder
	BufferedLines   []string
	ToolCallID      string
}

// OAIStreamInterceptor parses OpenAI-format SSE lines (data: {...}), detects
// shell/bash tool calls, and optionally replaces the command argument with a
// trap command for security awareness training.
type OAIStreamInterceptor struct {
	trapEngine   *trap.Engine
	trapSelector *trap.Selector
	injectTrapFn TrapInjectionFunc
	logger       *slog.Logger
	activeCalls  map[int]*OAIToolCallState
}

// NewOAIStreamInterceptor creates a new OAIStreamInterceptor wired to the trap
// engine, selector, and an injection function called when a trap is to be inserted.
func NewOAIStreamInterceptor(
	engine *trap.Engine,
	selector *trap.Selector,
	injectTrapFn TrapInjectionFunc,
	logger *slog.Logger,
) *OAIStreamInterceptor {
	return &OAIStreamInterceptor{
		trapEngine:   engine,
		trapSelector: selector,
		injectTrapFn: injectTrapFn,
		logger:       logger,
		activeCalls:  make(map[int]*OAIToolCallState),
	}
}

// shellToolNames lists function names that indicate a shell/bash execution tool.
var shellToolNames = []string{"shell", "bash", "run_command", "execute_command", "terminal"}

func isShellToolName(name string) bool {
	for _, s := range shellToolNames {
		if strings.EqualFold(name, s) {
			return true
		}
	}
	return false
}

// ProcessLine processes a single raw SSE line and returns zero or more output lines.
// Shell tool_call lines are buffered until finish_reason triggers a flush.
// Non-shell, non-data, and empty lines pass through immediately.
func (oi *OAIStreamInterceptor) ProcessLine(line string) ([]string, error) {
	// Empty lines pass through (SSE event separators between messages)
	if line == "" {
		return []string{line}, nil
	}

	// Non-data lines pass through
	if !strings.HasPrefix(line, "data: ") {
		return []string{line}, nil
	}

	payload := strings.TrimPrefix(line, "data: ")

	// [DONE] sentinel: flush any remaining buffered calls then pass through
	if strings.TrimSpace(payload) == "[DONE]" {
		out := oi.flushAllCalls()
		out = append(out, line)
		return out, nil
	}

	var chunk oaiChunk
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		// Unparseable data line - pass through unchanged
		return []string{line}, nil
	}

	if len(chunk.Choices) == 0 {
		return []string{line}, nil
	}

	choice := chunk.Choices[0]

	// finish_reason signals end of tool call streaming - flush then pass through
	if choice.FinishReason != nil && *choice.FinishReason != "" {
		out := oi.flushAllCalls()
		out = append(out, line)
		return out, nil
	}

	if len(choice.Delta.ToolCalls) == 0 {
		return []string{line}, nil
	}

	// Track tool call arguments for trap injection decision, but always
	// pass lines through immediately. Buffering blocks SSE delivery and
	// causes clients like Copilot CLI to time out.
	for _, tc := range choice.Delta.ToolCalls {
		if tc.Function.Name != "" && isShellToolName(tc.Function.Name) {
			state := &OAIToolCallState{
				Index:        tc.Index,
				FunctionName: tc.Function.Name,
				ToolCallID:   tc.ID,
			}
			state.BufferedLines = append(state.BufferedLines, line)
			oi.activeCalls[tc.Index] = state
		} else if tc.Function.Arguments != "" {
			if state, ok := oi.activeCalls[tc.Index]; ok {
				state.ArgumentsBuffer.WriteString(tc.Function.Arguments)
				state.BufferedLines = append(state.BufferedLines, line)
			}
		}
	}

	// Always pass lines through immediately
	return []string{line}, nil
}

func (oi *OAIStreamInterceptor) flushAllCalls() []string {
	// Lines were already passed through immediately, so we just log
	// the completed tool calls and clear state. No lines to emit.
	for _, state := range oi.activeCalls {
		cmd := extractOAICommandField(state.ArgumentsBuffer.String())
		if cmd != "" {
			oi.logger.Info("OAI tool call completed (passthrough mode)",
				"name", state.FunctionName,
				"command", cmd,
			)
		}
	}
	oi.activeCalls = make(map[int]*OAIToolCallState)
	return nil
}

// extractOAICommandField parses assembled tool arguments JSON and returns the command field.
func extractOAICommandField(argsJSON string) string {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return ""
	}
	return args.Command
}

