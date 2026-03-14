// Command 143-mcp is a Model Context Protocol (MCP) server that exposes
// integration tools (Sentry, Linear, Notion, Slack) to coding agents
// running inside sandboxes.
//
// It speaks JSON-RPC 2.0 over STDIO. The orchestrator starts one instance
// per agent sandbox, passing integration credentials as environment variables.
//
// Note: For sandbox agents, prefer 143-tools (CLI) over 143-mcp (MCP) —
// CLI is more token-efficient and reliable. MCP is kept for IDE integrations
// and interactive use cases.
//
// Usage:
//
//	SENTRY_AUTH_TOKEN=... SENTRY_ORG_SLUG=... LINEAR_ACCESS_TOKEN=... 143-mcp
//
// Logs are written to stderr. stdout is reserved for JSON-RPC messages.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/assembledhq/143/internal/services/mcp"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	reg := mcp.BuildRegistryFromEnv(os.Stderr)

	if !reg.HasAny() {
		fmt.Fprintln(os.Stderr, "143-mcp: warning: no integrations configured (check env vars)")
	}

	server := mcp.NewServer(reg, os.Stderr)

	if err := server.Serve(ctx, os.Stdin, os.Stdout); err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "143-mcp: shutting down")
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "143-mcp: fatal: %s\n", err)
		os.Exit(1)
	}
}
