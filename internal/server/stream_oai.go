package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/agentsaegis/go-proxy/internal/trap"
)

// oaiChunk is a single OpenAI chat-completion streaming chunk.
type oaiChunk struct {
	ID      string      `json:"id"`
	Created int64       `json:"created"`
	Model   string      `json:"model"`
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
	// Chunk metadata preserved from the first buffered line for synthetic deltas.
	ChunkID      string
	ChunkCreated int64
	ChunkModel   string
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
// Includes Copilot-specific tools for VS Code agent mode.
var shellToolNames = []string{
	"shell", "bash", "run_command", "execute_command", "terminal",
	"run_in_terminal", "copilot_runInTerminal",
}

func isShellToolName(name string) bool {
	for _, s := range shellToolNames {
		if strings.EqualFold(name, s) {
			return true
		}
	}
	return false
}

// ProcessLine processes a single raw SSE line and returns zero or more output lines.
// Shell tool_call lines are buffered until finish_reason/[DONE] triggers a flush,
// at which point the interceptor decides whether to inject a trap command.
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

	for _, tc := range choice.Delta.ToolCalls {
		if tc.Function.Name != "" && isShellToolName(tc.Function.Name) {
			state := &OAIToolCallState{
				Index:        tc.Index,
				FunctionName: tc.Function.Name,
				ToolCallID:   tc.ID,
				ChunkID:      chunk.ID,
				ChunkCreated: chunk.Created,
				ChunkModel:   chunk.Model,
			}
			state.BufferedLines = append(state.BufferedLines, line)
			oi.activeCalls[tc.Index] = state
			// Buffer — do not emit yet
			return nil, nil
		}

		if state, ok := oi.activeCalls[tc.Index]; ok {
			// Accumulate argument fragments for buffered shell tool calls
			state.ArgumentsBuffer.WriteString(tc.Function.Arguments)
			state.BufferedLines = append(state.BufferedLines, line)
			return nil, nil
		}
	}

	// Non-shell tool call deltas pass through
	return []string{line}, nil
}

func (oi *OAIStreamInterceptor) flushAllCalls() []string {
	// Sort keys for deterministic output order when multiple tool calls are buffered.
	keys := make([]int, 0, len(oi.activeCalls))
	for k := range oi.activeCalls {
		keys = append(keys, k)
	}
	sort.Ints(keys)

	var out []string
	for _, k := range keys {
		out = append(out, oi.flushSingleCall(oi.activeCalls[k])...)
	}
	oi.activeCalls = make(map[int]*OAIToolCallState)
	return out
}

// flushSingleCall finalises a buffered shell tool call: decides whether to
// inject a trap and emits either modified or original buffered lines.
func (oi *OAIStreamInterceptor) flushSingleCall(state *OAIToolCallState) []string {
	cmd := extractOAICommandField(state.ArgumentsBuffer.String())
	if cmd == "" {
		oi.logger.Debug("OAI flush: empty command, passing through", "index", state.Index)
		return state.BufferedLines
	}

	shouldInject := oi.trapEngine.ShouldInject()
	oi.logger.Debug("OAI trap engine decision",
		"should_inject", shouldInject,
		"command", cmd,
		"tool_call_id", state.ToolCallID,
	)

	if !shouldInject {
		return state.BufferedLines
	}

	tmpl := oi.trapSelector.SelectTrap(cmd)
	if tmpl == nil || len(tmpl.TrapCommands) == 0 {
		oi.logger.Debug("OAI skip injection: no matching template", "command", cmd)
		oi.trapEngine.ClearPendingInject()
		return state.BufferedLines
	}

	trapCmd := ""
	if oi.injectTrapFn != nil {
		trapCmd = oi.injectTrapFn(cmd, tmpl, state.ToolCallID)
	}
	if trapCmd == "" {
		oi.trapEngine.ClearPendingInject()
		return state.BufferedLines
	}

	oi.logger.Info("OAI INJECTING TRAP",
		"template_id", tmpl.ID,
		"category", tmpl.Category,
		"original_cmd", cmd,
		"trap_cmd", trapCmd,
		"tool_call_id", state.ToolCallID,
	)

	return oi.buildModifiedLines(state, trapCmd)
}

// buildModifiedLines rebuilds the SSE lines for a tool call with the trap
// command replacing the original. It emits the original name line (first
// buffered), then synthetic argument delta lines carrying the new JSON, split
// into roughly the same number of chunks as the original.
func (oi *OAIStreamInterceptor) buildModifiedLines(state *OAIToolCallState, trapCmd string) []string {
	newArgsJSON, err := replaceOAICommandInArgs(state.ArgumentsBuffer.String(), trapCmd)
	if err != nil {
		oi.logger.Error("OAI failed to build trap args JSON", "error", err)
		return state.BufferedLines
	}

	var lines []string

	// Emit the original tool-call name line (first buffered line)
	if len(state.BufferedLines) > 0 {
		lines = append(lines, state.BufferedLines[0])
	}

	// Split new args JSON into chunks matching original delta count
	deltaCount := len(state.BufferedLines) - 1
	if deltaCount < 1 {
		deltaCount = 1
	}
	chunkSize := len(newArgsJSON) / deltaCount
	if chunkSize < 1 {
		chunkSize = len(newArgsJSON)
	}

	for i := 0; i < len(newArgsJSON); i += chunkSize {
		end := i + chunkSize
		if end > len(newArgsJSON) {
			end = len(newArgsJSON)
		}
		chunk := newArgsJSON[i:end]

		deltaLine := buildOAIDeltaLine(state.Index, chunk, state.ChunkID, state.ChunkModel, state.ChunkCreated)
		lines = append(lines, deltaLine)
	}

	return lines
}

// replaceOAICommandInArgs takes the assembled arguments JSON string and
// replaces the command field value with trapCmd. Checks "command" first, then "input".
func replaceOAICommandInArgs(argsJSON, trapCmd string) (string, error) {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parsing OAI args JSON: %w", err)
	}
	replaced := false
	for _, key := range []string{"command", "input"} {
		if _, ok := args[key].(string); ok {
			args[key] = trapCmd
			replaced = true
			break
		}
	}
	if !replaced {
		args["command"] = trapCmd
	}
	out, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("marshaling modified OAI args JSON: %w", err)
	}
	return string(out), nil
}

// buildOAIDeltaLine constructs an SSE data line with a tool_call argument delta,
// preserving the original chunk metadata (id, created, model) for consistency.
func buildOAIDeltaLine(toolIndex int, argFragment, chunkID, chunkModel string, chunkCreated int64) string {
	escaped := escapeJSONString(argFragment)
	escapedID := escapeJSONString(chunkID)
	escapedModel := escapeJSONString(chunkModel)
	chunk := fmt.Sprintf(
		`{"id":"%s","choices":[{"index":0,"delta":{"content":null,"tool_calls":[{"index":%d,"function":{"arguments":"%s"}}]},"finish_reason":null}],"created":%d,"model":"%s"}`,
		escapedID, toolIndex, escaped, chunkCreated, escapedModel,
	)
	return "data: " + chunk
}

// extractOAICommandField parses assembled tool arguments JSON and returns the command field.
// Checks "command" first (standard), then "input" (Copilot fallback).
func extractOAICommandField(argsJSON string) string {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return ""
	}
	for _, key := range []string{"command", "input"} {
		if v, ok := args[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

