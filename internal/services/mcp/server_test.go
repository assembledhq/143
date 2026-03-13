package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/services/integration"
)

func TestServerInitializeHandshake(t *testing.T) {
	reg := integration.NewRegistry()
	srv := NewServer(reg, &bytes.Buffer{})

	// Send initialize request.
	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test"}}}`
	input := strings.NewReader(req + "\n")
	var output bytes.Buffer

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run in a goroutine since Serve blocks.
	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(ctx, input, &output)
	}()

	// Wait for EOF to close the serve loop.
	err := <-done
	if err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	var resp Response
	if err := json.Unmarshal(output.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v (raw: %s)", err, output.String())
	}

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	// Verify the result contains expected fields.
	resultJSON, _ := json.Marshal(resp.Result)
	var result InitializeResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}

	if result.ProtocolVersion != ProtocolVersion {
		t.Errorf("protocol version = %q, want %q", result.ProtocolVersion, ProtocolVersion)
	}
	if result.ServerInfo.Name != ServerName {
		t.Errorf("server name = %q, want %q", result.ServerInfo.Name, ServerName)
	}
	if result.Capabilities.Tools == nil {
		t.Error("tools capability is nil, expected non-nil")
	}
}

func TestServerToolsListRequiresInit(t *testing.T) {
	reg := integration.NewRegistry()
	srv := NewServer(reg, &bytes.Buffer{})

	req := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n"
	input := strings.NewReader(req)
	var output bytes.Buffer

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(ctx, input, &output)
	}()

	<-done

	var resp Response
	if err := json.Unmarshal(output.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected error for tools/list before initialize, got nil")
	}
	if resp.Error.Code != ErrCodeInvalidRequest {
		t.Errorf("error code = %d, want %d", resp.Error.Code, ErrCodeInvalidRequest)
	}
}

func TestServerToolsListEmpty(t *testing.T) {
	reg := integration.NewRegistry()
	srv := NewServer(reg, &bytes.Buffer{})

	// Initialize first, then list tools.
	lines := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}
	input := strings.NewReader(strings.Join(lines, "\n") + "\n")
	var output bytes.Buffer

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(context.Background(), input, &output)
	}()
	<-done

	// Parse both responses (newline-delimited JSON).
	decoder := json.NewDecoder(&output)
	var initResp, listResp Response
	if err := decoder.Decode(&initResp); err != nil {
		t.Fatalf("failed to parse init response: %v", err)
	}
	if err := decoder.Decode(&listResp); err != nil {
		t.Fatalf("failed to parse list response: %v", err)
	}

	if listResp.Error != nil {
		t.Fatalf("unexpected error: %v", listResp.Error)
	}

	resultJSON, _ := json.Marshal(listResp.Result)
	var result ToolsListResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}

	if len(result.Tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(result.Tools))
	}
}

func TestServerPing(t *testing.T) {
	reg := integration.NewRegistry()
	srv := NewServer(reg, &bytes.Buffer{})

	lines := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
	}
	input := strings.NewReader(strings.Join(lines, "\n") + "\n")
	var output bytes.Buffer

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(context.Background(), input, &output)
	}()
	<-done

	decoder := json.NewDecoder(&output)
	var initResp, pingResp Response
	_ = decoder.Decode(&initResp)
	if err := decoder.Decode(&pingResp); err != nil {
		t.Fatalf("failed to parse ping response: %v", err)
	}
	if pingResp.Error != nil {
		t.Fatalf("unexpected error: %v", pingResp.Error)
	}
}

func TestServerUnknownMethod(t *testing.T) {
	reg := integration.NewRegistry()
	srv := NewServer(reg, &bytes.Buffer{})

	lines := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/list"}`,
	}
	input := strings.NewReader(strings.Join(lines, "\n") + "\n")
	var output bytes.Buffer

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(context.Background(), input, &output)
	}()
	<-done

	decoder := json.NewDecoder(&output)
	var initResp, unknownResp Response
	_ = decoder.Decode(&initResp)
	if err := decoder.Decode(&unknownResp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if unknownResp.Error == nil {
		t.Fatal("expected error for unknown method, got nil")
	}
	if unknownResp.Error.Code != ErrCodeMethodNotFound {
		t.Errorf("error code = %d, want %d", unknownResp.Error.Code, ErrCodeMethodNotFound)
	}
}

func TestServerParseError(t *testing.T) {
	reg := integration.NewRegistry()
	srv := NewServer(reg, &bytes.Buffer{})

	input := strings.NewReader("this is not json\n")
	var output bytes.Buffer

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(context.Background(), input, &output)
	}()
	<-done

	var resp Response
	if err := json.Unmarshal(output.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected parse error, got nil")
	}
	if resp.Error.Code != ErrCodeParse {
		t.Errorf("error code = %d, want %d", resp.Error.Code, ErrCodeParse)
	}
}
