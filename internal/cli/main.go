package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/services/mcp"
	"github.com/assembledhq/143/internal/services/sandboxauth"
)

// Main is the 143-tools entry point. Dispatch order:
//
//  1. Sandbox infrastructure subcommands (git-credential, auth-token, ...).
//  2. Laptop commands (login, logout, whoami, update, setup, preview, mcp).
//  3. Integration tools (`143-tools <namespace> <action>`), with the
//     execution backend selected automatically: sandbox env credentials
//     present → direct mode (existing behavior, unchanged); config-file
//     token present → server-proxied mode. Same binary, no flags.
func Main() {
	args, overrides := extractGlobalFlags(os.Args[1:])

	if handled, code := sandboxauth.HandleSubcommand(args, os.Stdin, os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}
	if handled, code := HandleSubcommand(args, os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}

	source, err := selectToolSource(context.Background(), overrides)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
	os.Exit(mcp.RunCLI(context.Background(), source, args, os.Stdout, os.Stderr))
}

// globalOverrides are the leading `--token` / `--server` flags accepted
// before any subcommand, for scripted use (CI jobs that inject a token
// without writing a config file). Credential precedence: sandbox env vars →
// --token flag → config file.
type globalOverrides struct {
	token  string
	server string
}

// extractGlobalFlags strips leading --token/--server flags (both `--flag
// value` and `--flag=value` forms) from args. Only leading flags are
// consumed — flags after the first positional argument belong to the
// subcommand or tool being invoked.
func extractGlobalFlags(args []string) ([]string, globalOverrides) {
	var overrides globalOverrides
	for len(args) > 0 {
		switch {
		case args[0] == "--token" && len(args) > 1:
			overrides.token = args[1]
			args = args[2:]
		case strings.HasPrefix(args[0], "--token="):
			overrides.token = strings.TrimPrefix(args[0], "--token=")
			args = args[1:]
		case args[0] == "--server" && len(args) > 1:
			overrides.server = args[1]
			args = args[2:]
		case strings.HasPrefix(args[0], "--server="):
			overrides.server = strings.TrimPrefix(args[0], "--server=")
			args = args[1:]
		default:
			return args, overrides
		}
	}
	return args, overrides
}

// runMCPServe starts the stdio MCP server local agents register with
// (`claude mcp add 143 -- 143-tools mcp serve`). Backend selection follows
// the same rule as the CLI tool dispatch.
func runMCPServe(stderr io.Writer) int {
	source, err := selectToolSource(context.Background(), globalOverrides{})
	if err != nil {
		fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	server := mcp.NewServerWithSource(source, stderr)
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	return 0
}

// selectToolSource picks the execution backend. Inside a sandbox — or
// whenever provider env credentials are present — tools run directly with
// those credentials (the existing model). Otherwise, a logged-in config
// proxies every call through the 143 server, where org credentials live
// and per-user audit happens.
func selectToolSource(ctx context.Context, overrides globalOverrides) (mcp.ToolSource, error) {
	envRegistry := mcp.BuildRegistryFromEnv(os.Stderr)
	var direct mcp.ToolSource = mcp.NewToolRegistry(envRegistry)
	if token, apiURL := os.Getenv("INTERNAL_API_TOKEN"), os.Getenv("INTERNAL_API_URL"); token != "" && apiURL != "" {
		direct = mcp.NewInternalMetaToolSource(direct, token, apiURL)
		snapshot, err := mcp.FetchCapabilitySnapshot(ctx, token, apiURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "143-tools: capability snapshot unavailable, running without filter: %v\n", err)
		} else if len(snapshot) > 0 {
			direct = mcp.NewCapabilityFilteredToolSource(direct, mcp.ToolCapabilityPolicy{Capabilities: snapshot})
		}
	}
	if InSandbox() || len(direct.ListTools()) > 0 {
		return direct, nil
	}

	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	if overrides.token != "" {
		cfg.Token = overrides.token
	}
	if overrides.server != "" {
		cfg.ServerURL = strings.TrimRight(overrides.server, "/")
	}
	if cfg.ServerURL == "" || cfg.Token == "" {
		// No env credentials and no login: return the (empty) direct
		// registry so help output explains how to configure, matching the
		// historical behavior of running outside a sandbox.
		return direct, nil
	}

	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	remote, err := NewRemoteToolSource(fetchCtx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w (try `143-tools login`)", cfg.ServerURL, err)
	}
	return remote, nil
}
