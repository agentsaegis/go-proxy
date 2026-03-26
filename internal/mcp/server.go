package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"time"
)

// JSONRPCRequest is a JSON-RPC 2.0 request message.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse is a JSON-RPC 2.0 response message.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError is a JSON-RPC 2.0 error object.
type JSONRPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// initializeResult is the response to the initialize method.
type initializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    serverCapabilities `json:"capabilities"`
	ServerInfo      serverInfo         `json:"serverInfo"`
}

type serverCapabilities struct {
	Tools *toolsCapability `json:"tools,omitempty"`
}

type toolsCapability struct{}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Server handles MCP protocol communication over stdio.
type Server struct {
	checker   HookChecker
	logger    *slog.Logger
	reader    io.Reader
	writer    io.Writer
	sessionID string
}

// New creates a new MCP server.
func New(checker HookChecker, logger *slog.Logger, reader io.Reader, writer io.Writer) *Server {
	return &Server{
		checker:   checker,
		logger:    logger,
		reader:    reader,
		writer:    writer,
		sessionID: fmt.Sprintf("mcp_%d", time.Now().UnixNano()),
	}
}

// Run reads JSON-RPC messages from the reader and dispatches them.
// Blocks until the reader is closed or context is cancelled.
func (s *Server) Run(ctx context.Context) error {
	scanner := bufio.NewScanner(s.reader)
	// Allow up to 1MB per line (matching proxy's buffer size)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("reading stdin: %w", err)
			}
			// EOF - client disconnected
			return nil
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req JSONRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.logger.Debug("failed to parse JSON-RPC request", "error", err)
			if err := s.writeResponse(&JSONRPCResponse{
				JSONRPC: "2.0",
				Error:   &JSONRPCError{Code: -32700, Message: "parse error: " + err.Error()},
			}); err != nil {
				return fmt.Errorf("writing parse error response: %w", err)
			}
			continue
		}

		s.logger.Debug("received request", "method", req.Method, "id", string(req.ID))

		resp := s.dispatch(ctx, &req)
		if resp == nil {
			// Notification - no response needed
			continue
		}

		if err := s.writeResponse(resp); err != nil {
			return fmt.Errorf("writing response: %w", err)
		}
	}
}

func (s *Server) dispatch(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	// Notifications have no ID - don't send a response
	isNotification := req.ID == nil || string(req.ID) == "null"

	switch req.Method {
	case "initialize":
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: initializeResult{
				ProtocolVersion: "2024-11-05",
				Capabilities: serverCapabilities{
					Tools: &toolsCapability{},
				},
				ServerInfo: serverInfo{
					Name:    "agentsaegis",
					Version: "0.1.0",
				},
			},
		}

	case "notifications/initialized":
		// Notification - no response
		return nil

	case "tools/list":
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  s.toolsList(),
		}

	case "tools/call":
		result, rpcErr := s.toolsCall(ctx, req.Params)
		if rpcErr != nil {
			return &JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   rpcErr,
			}
		}
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  result,
		}

	default:
		if isNotification {
			// Unknown notification - ignore
			s.logger.Debug("ignoring unknown notification", "method", req.Method)
			return nil
		}
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &JSONRPCError{Code: -32601, Message: "method not found: " + req.Method},
		}
	}
}

func (s *Server) writeResponse(resp *JSONRPCResponse) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshaling response: %w", err)
	}
	data = append(data, '\n')
	_, err = s.writer.Write(data)
	return err
}
