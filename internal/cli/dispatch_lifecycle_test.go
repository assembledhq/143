package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/sandboxauth"
	"github.com/stretchr/testify/require"
)

func TestGlobalFlagOwnership(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		input, expected []string
		overrides       globalOverrides
	}{
		{name: "none"},
		{name: "leading separated flags", input: []string{"--token", "credential", "--server", "https://example.test", "preview", "list"}, expected: []string{"preview", "list"}, overrides: globalOverrides{token: "credential", server: "https://example.test"}},
		{name: "leading equals flags", input: []string{"--server=https://example.test", "--token=credential", "preview"}, expected: []string{"preview"}, overrides: globalOverrides{token: "credential", server: "https://example.test"}},
		{name: "subcommand owns trailing flags", input: []string{"preview", "--token", "subcommand-value"}, expected: []string{"preview", "--token", "subcommand-value"}},
		{name: "missing leading value stays visible", input: []string{"--token"}, expected: []string{"--token"}},
		{name: "last leading override wins", input: []string{"--token=old", "--token=new", "list"}, expected: []string{"list"}, overrides: globalOverrides{token: "new"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			args, overrides := extractGlobalFlags(tt.input)
			require.Equal(t, tt.expected, args, "global parsing must preserve subcommand-owned arguments")
			require.Equal(t, tt.overrides, overrides, "global parsing must retain exact leading credential and server overrides")
		})
	}
}

func clearDispatchEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{"SENTRY_AUTH_TOKEN", "LINEAR_ACCESS_TOKEN", "PAGERDUTY_ACCESS_TOKEN", "NOTION_ACCESS_TOKEN", "INTERNAL_API_TOKEN", "INTERNAL_API_URL", "CIRCLECI_TOKEN", "VICTORIALOGS_URL", "MEZMO_API_KEY", "GITHUB_REPO_OWNER", "GITHUB_REPO_NAME", models.ToolAllowlistEnvVar, sandboxauth.SocketEnvVar} {
		t.Setenv(key, "")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

// Backend selection loads process-wide credentials; isolated cases must serialize.
func TestToolBackendCredentialPrecedence(t *testing.T) {
	tests := []struct {
		name                                      string
		override, noConfig, fail, sandbox, direct bool
	}{
		{name: "saved credential selects gateway"}, {name: "explicit flags replace saved credential", override: true},
		{name: "no login returns empty help registry", noConfig: true}, {name: "gateway failure remains visible", fail: true},
		{name: "sandbox environment wins over laptop config", sandbox: true}, {name: "provider environment wins over laptop config", direct: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearDispatchEnvironment(t)
			var mu sync.Mutex
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requests = append(requests, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
				mu.Unlock()
				if tt.fail {
					http.Error(w, "unavailable", 503)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if _, err := io.WriteString(w, `{"data":{"tools":[]}}`); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			cfg := Config{ServerURL: server.URL, Token: "saved-token"}
			if !tt.noConfig {
				require.NoError(t, SaveConfig(cfg), "seed isolated laptop credentials")
			}
			overrides := globalOverrides{}
			if tt.override {
				overrides = globalOverrides{token: "override-token", server: server.URL + "///"}
			}
			if tt.sandbox {
				t.Setenv("INTERNAL_API_TOKEN", "sandbox-token")
			}
			if tt.direct {
				t.Setenv("LINEAR_ACCESS_TOKEN", "fixture-provider-token")
			}
			source, err := selectToolSource(context.Background(), overrides)
			if tt.fail {
				require.ErrorContains(t, err, "HTTP 503", "unavailable gateway must not silently select a different backend")
				require.Nil(t, source, "failed backend selection must not expose a fallback registry")
			} else {
				require.NoError(t, err, "configured backend must resolve")
				_, remote := source.(*RemoteToolSource)
				require.Equal(t, !tt.noConfig && !tt.sandbox && !tt.direct, remote, "sandbox and provider credentials must precede laptop credentials")
				if tt.noConfig || tt.sandbox {
					require.Empty(t, source.ListTools(), "unconfigured direct backend must offer no fabricated tools")
				}
				if tt.direct {
					require.NotEmpty(t, source.ListTools(), "configured provider must expose its direct integration tools")
				}
			}
			var expected []string
			if !tt.noConfig && !tt.sandbox && !tt.direct {
				token := "saved-token"
				if tt.override {
					token = "override-token"
				}
				expected = []string{"GET /api/v1/cli/tools Bearer " + token}
			}
			mu.Lock()
			actual := append([]string(nil), requests...)
			mu.Unlock()
			require.Equal(t, expected, actual, "only gateway selection may send laptop credentials to the configured endpoint")
		})
	}
}

// Preview commands read a process-wide config; these local-server cases serialize.
func TestLaptopPreviewCommandDispatch(t *testing.T) {
	tests := []struct {
		name                          string
		args                          []string
		method, path, reply, expected string
		status, code                  int
	}{
		{name: "list", args: []string{"list"}, method: "GET", path: "/api/v1/previews", reply: `[]`, expected: `[]`},
		{name: "stop shorthand", args: []string{"stop", "branch"}, method: "POST", path: "/api/v1/previews/branch/stop", reply: `{}`, expected: `{"preview_id":"branch","status":"stopping"}`},
		{name: "console via tool schema", args: []string{"console", "--preview-id", "branch", "--level", "error"}, method: "GET", path: "/api/v1/previews/branch/console?level=error", reply: `[]`, expected: `[]`},
		{name: "control via tool schema", args: []string{"control", "--session-id", "session"}, method: "GET", path: "/api/v1/sessions/session/preview/control", reply: `{"owner":"human"}`, expected: `{"owner":"human"}`},
		{name: "unknown subcommand", args: []string{"unknown"}, code: 1, expected: "unknown preview subcommand"},
		{name: "backend fails", args: []string{"list"}, method: "GET", path: "/api/v1/previews", status: 503, code: 1, expected: "503"},
		{name: "missing target rejected", args: []string{"stop"}, code: 1, expected: "required"},
		{name: "help bypasses credentials", args: []string{"--help"}, expected: "Usage:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearDispatchEnvironment(t)
			var mu sync.Mutex
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requests = append(requests, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Authorization"))
				mu.Unlock()
				if tt.status != 0 {
					w.WriteHeader(tt.status)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if _, err := io.WriteString(w, `{"data":`+tt.reply+`}`); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			if tt.name != "help bypasses credentials" {
				require.NoError(t, SaveConfig(Config{ServerURL: server.URL, Token: "user-token"}), "seed isolated preview credentials")
			}
			var stdout, stderr bytes.Buffer
			code := runPreview(tt.args, &stdout, &stderr)
			require.Equal(t, tt.code, code, "preview command must preserve failure exit codes")
			if tt.code != 0 {
				require.Contains(t, stderr.String(), tt.expected, "failed preview command must explain rejection")
				require.Empty(t, stdout.String(), "failed preview command must not print success")
			} else if tt.name == "help bypasses credentials" {
				require.Contains(t, stdout.String(), tt.expected, "help must remain available before login")
				require.Empty(t, stderr.String(), "help must not report a credential failure")
			} else {
				require.JSONEq(t, tt.expected, strings.TrimSpace(stdout.String()), "preview command must print exact lifecycle result")
				require.Empty(t, stderr.String(), "successful preview command must not print errors")
			}
			var expected []string
			if tt.path != "" {
				expected = []string{tt.method + " " + tt.path + " Bearer user-token"}
			}
			mu.Lock()
			actual := append([]string(nil), requests...)
			mu.Unlock()
			require.Equal(t, expected, actual, "CLI dispatch must send exactly one correctly authenticated lifecycle request")
		})
	}
}
