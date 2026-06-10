package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/assembledhq/143/internal/services/integration"
)

const (
	// ProtocolVersion is the MCP protocol version this server implements.
	ProtocolVersion = "2024-11-05"

	// ServerName identifies this MCP server to clients.
	ServerName = "143-integrations"

	// ServerVersion is the semantic version of this MCP server.
	ServerVersion = "0.1.0"
)

// Server is a Model Context Protocol server that exposes integration tools
// over STDIO using JSON-RPC 2.0.
//
// It reads newline-delimited JSON-RPC requests from stdin and writes
// responses to stdout. Logging goes to a separate writer (typically stderr)
// to keep the STDIO transport clean.
type Server struct {
	tools  ToolSource
	logger io.Writer // stderr for diagnostic logging

	mu          sync.Mutex
	initialized bool
}

// NewServer creates a new MCP server backed by the given integration registry.
// The logger should be stderr — stdout is reserved for JSON-RPC messages.
func NewServer(reg *integration.Registry, logger io.Writer) *Server {
	return NewServerWithSource(NewToolRegistry(reg), logger)
}

// NewServerWithSource creates an MCP server over any ToolSource — the local
// integration registry in sandboxes, or the server-proxied source used by
// `143-tools mcp serve` on laptops (where org credentials never exist
// locally and every call flows through the 143 server).
func NewServerWithSource(tools ToolSource, logger io.Writer) *Server {
	return &Server{
		tools:  tools,
		logger: logger,
	}
}

// Serve runs the MCP server, reading JSON-RPC requests from r and writing
// responses to w. It blocks until ctx is cancelled or r reaches EOF.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)

	// MCP messages can be large (tool results with full documents).
	const maxMessageSize = 10 * 1024 * 1024 // 10 MB
	scanner.Buffer(make([]byte, 0, 64*1024), maxMessageSize)

	encoder := json.NewEncoder(w)

	// Note: scanner.Scan() blocks and does not respect context cancellation.
	// This is acceptable for STDIO transport — when the parent process kills
	// us, stdin is closed, which unblocks the scanner and causes a clean exit.
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("read error: %w", err)
			}
			return nil // EOF — clean shutdown
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue // skip blank lines
		}

		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			resp := Response{
				JSONRPC: "2.0",
				Error:   &RPCError{Code: ErrCodeParse, Message: "parse error: " + err.Error()},
			}
			if err := encoder.Encode(resp); err != nil {
				return fmt.Errorf("write error: %w", err)
			}
			continue
		}

		resp := s.handleRequest(ctx, &req)
		if resp == nil {
			continue // notification — no response needed
		}

		if err := encoder.Encode(resp); err != nil {
			return fmt.Errorf("write error: %w", err)
		}
	}
}

// handleRequest routes a JSON-RPC request to the appropriate handler.
func (s *Server) handleRequest(ctx context.Context, req *Request) *Response {
	// Notifications (no ID) don't get responses per JSON-RPC spec.
	isNotification := len(req.ID) == 0

	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)

	case "initialized":
		// Client acknowledgment notification — no response.
		return nil

	case "notifications/cancelled":
		// Client cancelled a request — no response.
		return nil

	case "ping":
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{},
		}

	case "tools/list":
		if !s.isInitialized() {
			return s.errorResponse(req.ID, ErrCodeInvalidRequest, "server not initialized")
		}
		return s.handleToolsList(req)

	case "tools/call":
		if !s.isInitialized() {
			return s.errorResponse(req.ID, ErrCodeInvalidRequest, "server not initialized")
		}
		return s.handleToolsCall(ctx, req)

	default:
		if isNotification {
			return nil
		}
		return s.errorResponse(req.ID, ErrCodeMethodNotFound, fmt.Sprintf("method not found: %s", req.Method))
	}
}

// handleInitialize processes the MCP initialization handshake.
func (s *Server) handleInitialize(req *Request) *Response {
	s.mu.Lock()
	s.initialized = true
	s.mu.Unlock()

	result := InitializeResult{
		ProtocolVersion: ProtocolVersion,
		Capabilities: ServerCapability{
			Tools: &ToolCapability{},
		},
		ServerInfo: ServerInfo{
			Name:    ServerName,
			Version: ServerVersion,
		},
	}

	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}
}

// handleToolsList returns the list of available tools.
func (s *Server) handleToolsList(req *Request) *Response {
	tools := s.tools.ListTools()
	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  ToolsListResult{Tools: tools},
	}
}

// handleToolsCall dispatches a tool call to the integration layer.
func (s *Server) handleToolsCall(ctx context.Context, req *Request) *Response {
	var params ToolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return s.errorResponse(req.ID, ErrCodeInvalidParams, "invalid tool call params: "+err.Error())
	}

	if params.Name == "" {
		return s.errorResponse(req.ID, ErrCodeInvalidParams, "tool name is required")
	}

	result := s.tools.CallTool(ctx, params.Name, params.Arguments)

	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}
}

// isInitialized returns true if the client has completed the handshake.
func (s *Server) isInitialized() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.initialized
}

// errorResponse creates an error response.
func (s *Server) errorResponse(id json.RawMessage, code int, msg string) *Response {
	return &Response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &RPCError{Code: code, Message: msg},
	}
}
