package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/agentsaegis/go-proxy/internal/trap"
)

const (
	maxOutputBytes = 1 << 20 // 1MB
	cmdTimeout     = 120 * time.Second
)

// ToolResult is the MCP tool execution result.
type ToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ContentBlock is a single content block in a tool result.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// toolDefinition describes a tool for tools/list.
type toolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// toolsListResult is the response for tools/list.
type toolsListResult struct {
	Tools []toolDefinition `json:"tools"`
}

// toolsCallParams is the params for tools/call.
type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

var bashToolSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"command": {
			"type": "string",
			"description": "The bash command to execute"
		}
	},
	"required": ["command"]
}`)

func (s *Server) toolsList() interface{} {
	return toolsListResult{
		Tools: []toolDefinition{
			{
				Name:        "bash",
				Description: "Execute a bash command",
				InputSchema: bashToolSchema,
			},
		},
	}
}

func (s *Server) toolsCall(ctx context.Context, params json.RawMessage) (interface{}, *JSONRPCError) {
	var p toolsCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &JSONRPCError{Code: -32602, Message: "invalid params: " + err.Error()}
	}

	if p.Name != "bash" {
		return nil, &JSONRPCError{Code: -32602, Message: fmt.Sprintf("unknown tool: %s", p.Name)}
	}

	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(p.Arguments, &args); err != nil {
		return nil, &JSONRPCError{Code: -32602, Message: "invalid arguments: " + err.Error()}
	}

	if args.Command == "" {
		return nil, &JSONRPCError{Code: -32602, Message: "command is required"}
	}

	return s.executeBash(ctx, args.Command), nil
}

func (s *Server) executeBash(ctx context.Context, command string) *ToolResult {
	// Check with proxy hook endpoint
	hookResult, hookErr := s.checker.CheckCommand(ctx, s.sessionID, command, "")

	if hookErr != nil {
		// Hook unreachable - try trap file fallback
		s.logger.Debug("hook check failed, trying trap file fallback", "error", hookErr)
		if blocked, msg := s.checkTrapFiles(command); blocked {
			return &ToolResult{
				Content: []ContentBlock{{Type: "text", Text: msg}},
				IsError: true,
			}
		}
		// Fallback also clear - execute command (fail-open)
	} else if !hookResult.Allowed {
		// Hook denied the command - it's a trap
		s.logger.Info("command blocked by hook", "command_len", len(command))
		return &ToolResult{
			Content: []ContentBlock{{Type: "text", Text: formatTrainingMessage(command)}},
			IsError: true,
		}
	}

	// Execute the command
	cmdCtx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "/bin/bash", "-c", command)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	err := cmd.Run()

	out := output.String()
	if len(out) > maxOutputBytes {
		out = out[:maxOutputBytes] + "\n[output truncated at 1MB]"
	}

	if err != nil {
		if out == "" {
			out = err.Error()
		}
		return &ToolResult{
			Content: []ContentBlock{{Type: "text", Text: out}},
			IsError: true,
		}
	}

	if out == "" {
		out = "(no output)"
	}

	return &ToolResult{
		Content: []ContentBlock{{Type: "text", Text: out}},
	}
}

// checkTrapFiles checks command against on-disk trap files (fallback when proxy is down).
func (s *Server) checkTrapFiles(command string) (blocked bool, message string) {
	files, err := trap.ReadAllActiveTrapFiles()
	if err != nil || len(files) == 0 {
		return false, ""
	}

	for _, f := range files {
		result := trap.MatchCommand(command, f.TrapCommand)
		if result.Matched {
			s.logger.Info("command matched trap file (fallback)", "trap_id", f.ID)
			return true, formatTrainingMessage(f.TrapCommand)
		}
	}

	return false, ""
}

// formatTrainingMessage returns a plain text training message for MCP tool results.
func formatTrainingMessage(trapCmd string) string {
	var b strings.Builder
	b.WriteString("AGENTSAEGIS - Security Awareness Test\n")
	b.WriteString("======================================\n\n")
	b.WriteString("You approved a dangerous command without catching it.\n\n")
	b.WriteString("Command: ")
	b.WriteString(trapCmd)
	b.WriteString("\n\n")
	b.WriteString("The command was NOT executed. Your session continues.\n\n")
	b.WriteString("Tip: Always review commands carefully before approving them,\n")
	b.WriteString("especially those that modify files, access networks, or install packages.")
	return b.String()
}
