// Command 143-tools provides CLI access to integration tools (Sentry, Linear,
// Notion, Slack), to sandbox-internal helpers (git credential helper, gh
// token wrapper, post-clone bootstrap) for coding agents running inside
// sandboxes, and — on laptops — to the 143 server itself via browser login.
//
// Usage (integration tools — direct mode in sandboxes, server-proxied on
// logged-in laptops; backend selected automatically):
//
//	143-tools sentry list_errors --severity high --limit 10
//	143-tools --help
//
// Usage (laptop):
//
//	143-tools login [--server URL] [--join TOKEN] [--no-browser]
//	143-tools whoami | logout | update
//	143-tools preview create --wait
//	143-tools mcp serve                    # stdio MCP gateway for local agents
//
// Usage (sandbox infrastructure — invoked by git/gh/orchestrator, not the agent):
//
//	143-tools git-credential               # git credential.helper
//	143-tools auth-token [--action push|api]
//	143-tools git-bootstrap --workdir=/path/to/repo
package main

import (
	"github.com/assembledhq/143/internal/cli"
)

func main() {
	cli.Main()
}
