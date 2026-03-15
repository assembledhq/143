// Command 143-tools provides CLI access to integration tools (Sentry, Linear,
// Notion, Slack) for coding agents running inside sandboxes.
//
// Unlike the MCP server, this binary is invoked directly by the agent as a
// shell command — no JSON-RPC handshake, no schema overhead. The agent
// already knows how to use CLI flags from its training data.
//
// Usage:
//
//	SENTRY_AUTH_TOKEN=... LINEAR_ACCESS_TOKEN=... 143-tools <tool> [--flag value ...]
//	143-tools sentry_list_errors --severity high --limit 10
//	143-tools linear_create_task --title "Fix auth" --team_key ENG
//	143-tools --help
//
// Credentials are read from environment variables. Output is JSON to stdout.
// Errors go to stderr.
package main

import "github.com/assembledhq/143/internal/services/mcp"

func main() {
	mcp.MainCLI()
}
