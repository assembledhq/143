package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/services/sandboxauth"
	"github.com/assembledhq/143/internal/version"
)

// HandleSubcommand dispatches the laptop-side 143-tools commands. Returns
// handled=false when args don't name one of them, so the caller falls
// through to the integration-tool dispatch.
func HandleSubcommand(args []string, stdout, stderr io.Writer) (handled bool, exitCode int) {
	if len(args) == 0 {
		return false, 0
	}
	switch args[0] {
	case "login":
		if refuseInSandbox(args[0], stderr) {
			return true, 1
		}
		return true, runLogin(args[1:], stdout, stderr)
	case "logout":
		return true, runLogout(args[1:], stdout, stderr)
	case "whoami":
		return true, runWhoami(args[1:], stdout, stderr)
	case "update":
		if refuseInSandbox(args[0], stderr) {
			return true, 1
		}
		return true, runUpdate(args[1:], stdout, stderr)
	case "setup":
		if refuseInSandbox(args[0], stderr) {
			return true, 1
		}
		return true, runSetup(args[1:], stdout, stderr)
	case "preview":
		return true, runPreview(args[1:], stdout, stderr)
	case "mcp":
		if len(args) >= 2 && args[1] == "serve" {
			return true, runMCPServe(stderr)
		}
		fmt.Fprintln(stderr, "Usage: 143-tools mcp serve")
		return true, 1
	}
	return false, 0
}

// InSandbox reports whether this process is running inside a 143 sandbox.
// Same signal the sandbox-side git/auth commands key on (the auth socket),
// plus the internal API token every sandbox injects.
func InSandbox() bool {
	return os.Getenv(sandboxauth.SocketEnvVar) != "" || os.Getenv("INTERNAL_API_TOKEN") != ""
}

// refuseInSandbox blocks credential-minting and self-replacement commands
// inside sandboxes: agents must never be able to mint user credentials
// (login/setup) or swap out the tool binary under the orchestrator (update).
func refuseInSandbox(command string, stderr io.Writer) bool {
	if !InSandbox() {
		return false
	}
	fmt.Fprintf(stderr, "error: `143-tools %s` is disabled inside sandboxes\n", command)
	return true
}

// runSetup writes (or merges) the config file. Called by the install script
// with the server URL — and join token, on tokened installs — templated by
// the server. An existing login token for the same server is preserved so
// re-running the installer upgrades in place.
func runSetup(args []string, stdout, stderr io.Writer) int {
	var server, join string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--server":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "error: --server requires a value")
				return 1
			}
			i++
			server = strings.TrimRight(args[i], "/")
		case "--join":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "error: --join requires a value")
				return 1
			}
			i++
			join = args[i]
		case "--help", "-h":
			fmt.Fprintln(stdout, "Usage: 143-tools setup --server URL [--join TOKEN]")
			return 0
		default:
			fmt.Fprintf(stderr, "error: unknown flag %q\n", args[i])
			return 1
		}
	}
	if server == "" {
		fmt.Fprintln(stderr, "error: --server is required")
		return 1
	}

	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	if cfg.ServerURL != server {
		// Pointing at a different server invalidates the old credential
		// locally (it still exists server-side; logout would revoke it).
		cfg.Token = ""
		cfg.OrgID = ""
	}
	cfg.ServerURL = server
	if join != "" {
		cfg.PendingJoinToken = join
	}
	if err := SaveConfig(cfg); err != nil {
		fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Configured server %s\n", server)
	return 0
}

// runLogout revokes the current CLI token server-side, then clears it from
// the config file.
func runLogout(_ []string, stdout, stderr io.Writer) int {
	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	if cfg.Token == "" {
		fmt.Fprintln(stdout, "Not logged in.")
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := NewClient(cfg)

	// Find our own token row by prefix to revoke it server-side. Best
	// effort: clearing the local credential is the part logout must not
	// fail at.
	var resp struct {
		Data []struct {
			ID          string `json:"id"`
			TokenPrefix string `json:"token_prefix"`
		} `json:"data"`
	}
	if err := client.Do(ctx, http.MethodGet, "/api/v1/auth/cli-tokens", nil, &resp); err == nil {
		for _, t := range resp.Data {
			if strings.HasPrefix(cfg.Token, t.TokenPrefix) {
				if err := client.Do(ctx, http.MethodDelete, "/api/v1/auth/cli-tokens/"+t.ID, nil, nil); err != nil {
					fmt.Fprintf(stderr, "note: could not revoke token server-side: %s\n", err)
				}
				break
			}
		}
	} else {
		fmt.Fprintf(stderr, "note: could not revoke token server-side: %s\n", err)
	}

	cfg.Token = ""
	cfg.OrgID = ""
	if err := SaveConfig(cfg); err != nil {
		fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Logged out.")
	return 0
}

// runWhoami prints the authenticated identity. `--local` only checks the
// config file (used by the installer to decide whether to chain into login)
// and never touches the network.
func runWhoami(args []string, stdout, stderr io.Writer) int {
	local := false
	for _, a := range args {
		if a == "--local" {
			local = true
		}
	}

	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	if cfg.ServerURL == "" || cfg.Token == "" {
		fmt.Fprintln(stderr, "Not logged in — run `143-tools login`.")
		return 1
	}
	if local {
		fmt.Fprintf(stdout, "Logged in to %s (token %s...)\n", cfg.ServerURL, tokenPrefixForDisplay(cfg.Token))
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := NewClient(cfg)

	var me struct {
		Data struct {
			Email       string  `json:"email"`
			Name        string  `json:"name"`
			GitHubLogin *string `json:"github_login"`
		} `json:"data"`
	}
	if err := client.Do(ctx, http.MethodGet, "/api/v1/auth/me", nil, &me); err != nil {
		fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}

	identity := me.Data.Name
	if me.Data.GitHubLogin != nil && *me.Data.GitHubLogin != "" {
		identity = "@" + *me.Data.GitHubLogin
	}
	fmt.Fprintf(stdout, "User:    %s (%s)\n", identity, me.Data.Email)

	var memberships struct {
		Data struct {
			ActiveOrgID string `json:"active_org_id"`
			ActiveRole  string `json:"active_role"`
			Memberships []struct {
				OrgID   string `json:"org_id"`
				OrgName string `json:"org_name"`
			} `json:"memberships"`
		} `json:"data"`
	}
	if err := client.Do(ctx, http.MethodGet, "/api/v1/auth/memberships", nil, &memberships); err == nil {
		for _, m := range memberships.Data.Memberships {
			if m.OrgID == memberships.Data.ActiveOrgID {
				fmt.Fprintf(stdout, "Org:     %s (%s)\n", m.OrgName, memberships.Data.ActiveRole)
				break
			}
		}
	}
	fmt.Fprintf(stdout, "Token:   %s...\n", tokenPrefixForDisplay(cfg.Token))
	fmt.Fprintf(stdout, "Server:  %s\n", cfg.ServerURL)

	printUpdateHintIfStale(ctx, client, stdout)
	return 0
}

// printUpdateHintIfStale compares the CLI's embedded build version against
// the server's and prints the one-line update hint on mismatch.
func printUpdateHintIfStale(ctx context.Context, client *Client, stdout io.Writer) {
	var resp struct {
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := client.Do(ctx, http.MethodGet, "/api/v1/cli/version", nil, &resp); err != nil {
		return
	}
	if resp.Data.Version != "" && resp.Data.Version != version.BuildSHA {
		fmt.Fprintln(stdout, "\nA newer CLI is available — run `143-tools update`.")
	}
}

func tokenPrefixForDisplay(token string) string {
	if len(token) <= 13 {
		return token
	}
	return token[:13]
}
