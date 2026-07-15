// Package mcp implements a Model Context Protocol (MCP) server that exposes
// integration tools to coding agents running inside sandboxes.
//
// The server speaks JSON-RPC 2.0 over STDIO, which is the standard MCP
// transport for subprocess-based tool servers. Each agent sandbox gets its
// own MCP server process, ensuring complete isolation.
package mcp

import "encoding/json"

// --------------------------------------------------------------------------
// JSON-RPC 2.0 wire types
// --------------------------------------------------------------------------

// Request is a JSON-RPC 2.0 request.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // number or string; omitted for notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Standard JSON-RPC error codes.
const (
	ErrCodeParse          = -32700
	ErrCodeInvalidRequest = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternal       = -32603
)

// --------------------------------------------------------------------------
// MCP protocol types
// --------------------------------------------------------------------------

// InitializeParams is sent by the client in the "initialize" request.
type InitializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities,omitempty"`
	ClientInfo      ClientInfo     `json:"clientInfo,omitempty"`
}

// ClientInfo identifies the MCP client.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// InitializeResult is the server's response to "initialize".
type InitializeResult struct {
	ProtocolVersion string           `json:"protocolVersion"`
	Capabilities    ServerCapability `json:"capabilities"`
	ServerInfo      ServerInfo       `json:"serverInfo"`
}

// ServerCapability declares what the server supports.
type ServerCapability struct {
	Tools *ToolCapability `json:"tools,omitempty"`
}

// ToolCapability describes the server's tool support.
type ToolCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ServerInfo identifies the MCP server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// --------------------------------------------------------------------------
// MCP tool types
// --------------------------------------------------------------------------

// Tool describes a single MCP tool exposed to the agent.
type Tool struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	InputSchema ToolSchema `json:"inputSchema"`
}

// ToolSchema is a JSON Schema object describing the tool's parameters.
type ToolSchema struct {
	Type       string                    `json:"type"` // always "object"
	Properties map[string]SchemaProperty `json:"properties,omitempty"`
	Required   []string                  `json:"required,omitempty"`
}

// SchemaProperty is a single property in a JSON Schema object.
type SchemaProperty struct {
	Type        string          `json:"type"`
	Description string          `json:"description,omitempty"`
	Enum        []string        `json:"enum,omitempty"`
	Items       *SchemaProperty `json:"items,omitempty"` // for array types
	Default     any             `json:"default,omitempty"`
}

// ToolsListResult is the response to "tools/list".
type ToolsListResult struct {
	Tools []Tool `json:"tools"`
}

// ToolCallParams is the request body for "tools/call".
type ToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// ToolCallResult is the response to "tools/call".
type ToolCallResult struct {
	Content []ToolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// ToolContent is a single content block in a tool result.
type ToolContent struct {
	Type     string `json:"type"` // "text" or "image"
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
}

// TextResult creates a ToolCallResult with a single text block.
func TextResult(text string) *ToolCallResult {
	return &ToolCallResult{
		Content: []ToolContent{{Type: "text", Text: text}},
	}
}

// ImageContent creates an MCP image content block. Data must be base64-encoded
// image bytes; callers remain responsible for validating the encoded payload.
func ImageContent(data, mimeType string) ToolContent {
	return ToolContent{Type: "image", Data: data, MIMEType: mimeType}
}

// ErrorResult creates a ToolCallResult indicating an error.
func ErrorResult(msg string) *ToolCallResult {
	return &ToolCallResult{
		Content: []ToolContent{{Type: "text", Text: msg}},
		IsError: true,
	}
}
