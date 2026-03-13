// Command 143-mcp is a Model Context Protocol (MCP) server that exposes
// integration tools (Sentry, Linear, Notion, Slack) to coding agents
// running inside sandboxes.
//
// It speaks JSON-RPC 2.0 over STDIO. The orchestrator starts one instance
// per agent sandbox, passing integration credentials as environment variables.
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

	"github.com/assembledhq/143/internal/services/integration"
	"github.com/assembledhq/143/internal/services/mcp"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	reg := buildRegistry()

	if !reg.HasAny() {
		fmt.Fprintln(os.Stderr, "143-mcp: warning: no integrations configured (check env vars)")
	}

	summary := reg.Summary()
	for category, providers := range summary {
		fmt.Fprintf(os.Stderr, "143-mcp: %s: %v\n", category, providers)
	}

	server := mcp.NewServer(reg, os.Stderr)

	if err := server.Serve(ctx, os.Stdin, os.Stdout); err != nil {
		if ctx.Err() != nil {
			// Clean shutdown via signal.
			fmt.Fprintln(os.Stderr, "143-mcp: shutting down")
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "143-mcp: fatal: %s\n", err)
		os.Exit(1)
	}
}

// buildRegistry creates an integration registry from environment variables.
// Each integration is optional — only configured integrations are registered.
func buildRegistry() *integration.Registry {
	reg := integration.NewRegistry()

	// Sentry error tracker.
	if token := os.Getenv("SENTRY_AUTH_TOKEN"); token != "" {
		orgSlug := os.Getenv("SENTRY_ORG_SLUG")
		baseURL := os.Getenv("SENTRY_BASE_URL") // empty = default (https://sentry.io/api/0)

		tracker := integration.NewSentryErrorTracker(integration.SentryTrackerConfig{
			AuthToken: token,
			OrgSlug:   orgSlug,
			BaseURL:   baseURL,
		})
		reg.RegisterErrorTracker(tracker)
		fmt.Fprintf(os.Stderr, "143-mcp: registered sentry error tracker (org=%s)\n", orgSlug)
	}

	// Linear task manager.
	if token := os.Getenv("LINEAR_ACCESS_TOKEN"); token != "" {
		apiURL := os.Getenv("LINEAR_API_URL") // empty = default (https://api.linear.app/graphql)

		manager := integration.NewLinearTaskManager(integration.LinearManagerConfig{
			AuthToken: token,
			APIURL:    apiURL,
		})
		reg.RegisterTaskManager(manager)
		fmt.Fprintln(os.Stderr, "143-mcp: registered linear task manager")
	}

	// Future: Notion document store, Slack message source.
	// These follow the same pattern — check env var, instantiate, register.

	return reg
}
