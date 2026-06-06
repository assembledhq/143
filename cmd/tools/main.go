// Command 143-tools provides CLI access to integration tools (Sentry, Linear,
// Notion, Slack) and to sandbox-internal helpers (git credential helper, gh
// token wrapper, post-clone bootstrap) for coding agents running inside
// sandboxes.
//
// Usage (integration tools):
//
//	SENTRY_AUTH_TOKEN=... LINEAR_ACCESS_TOKEN=... 143-tools <namespace> <action> [--flag value ...]
//	143-tools sentry list_errors --severity high --limit 10
//	143-tools --help
//
// Usage (sandbox infrastructure — invoked by git/gh/orchestrator, not the agent):
//
//	143-tools git-credential               # git credential.helper
//	143-tools auth-token [--action push|api]
//	143-tools git-bootstrap --workdir=/path/to/repo
package main

import (
	"os"

	"github.com/assembledhq/143/internal/services/mcp"
	"github.com/assembledhq/143/internal/services/sandboxauth"
)

func main() {
	// Sandbox-side subcommands are dispatched first so they don't get
	// confused with integration tool names. They have no overlap with the
	// agent-facing namespaces, but explicit dispatch is clearer than relying
	// on naming convention.
	if handled, code := sandboxauth.HandleSubcommand(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}
	mcp.MainCLI()
}
