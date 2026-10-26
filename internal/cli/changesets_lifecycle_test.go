package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/assembledhq/143/internal/internalapi"
	"github.com/assembledhq/143/internal/services/sandboxauth"
	"github.com/stretchr/testify/require"
)

func TestChangesetCommandLifecycle(t *testing.T) {
	// These commands load process-wide credential and session environment variables.
	// Serial cases keep each request inside its own isolated credential config.
	tests := []struct {
		name                              string
		args                              []string
		method, path, body, reply, output string
		status                            int
		internal                          bool
	}{
		{name: "list", args: []string{"list"}, method: "GET", path: "/sessions/session/changesets", reply: `{"data":[]}`, output: `{"data":[]}`},
		{name: "current primary", args: []string{"current"}, method: "GET", path: "/sessions/session/changesets", reply: `{"data":[{"id":"other","is_primary":false},{"id":"primary","is_primary":true}]}`, output: `{"id":"primary","is_primary":true}`},
		{name: "current explicitly selected", args: []string{"current"}, method: "GET", path: "/sessions/session/changesets", reply: `{"data":[{"id":"selected","is_primary":false},{"id":"primary","is_primary":true}]}`, output: `{"id":"selected","is_primary":false}`},
		{name: "current missing selection returns inventory", args: []string{"current"}, method: "GET", path: "/sessions/session/changesets", reply: `{"data":[]}`, output: `{"data":[]}`},
		{name: "status", args: []string{"status"}, method: "GET", path: "/sessions/session/changesets/split-status", reply: `{"data":{"ready":true}}`, output: `{"data":{"ready":true}}`},
		{name: "split alias", args: []string{"split-status"}, method: "GET", path: "/sessions/session/changesets/split-status", reply: `{"data":{"ready":true}}`, output: `{"data":{"ready":true}}`},
		{name: "public diff", args: []string{"diff", "--changeset", "change"}, method: "GET", path: "/sessions/session/diff?changeset_id=change", reply: `{"data":{"diff":"+fix"}}`, output: `{"data":{"diff":"+fix"}}`},
		{name: "internal diff", args: []string{"diff", "--changeset", "change"}, method: "GET", path: "/sessions/session/changesets/diff?changeset_id=change", internal: true, reply: `{"data":{"diff":"+fix"}}`, output: `{"data":{"diff":"+fix"}}`},
		{name: "create standalone", args: []string{"create", "--title", "Fix retries"}, method: "POST", path: "/sessions/session/changesets", body: `{"title":"Fix retries"}`, reply: `{"data":{"id":"change"}}`, output: `{"data":{"id":"change"}}`},
		{name: "create stacked", args: []string{"create", "--title", "Fix retries", "--stacked-on", "parent"}, method: "POST", path: "/sessions/session/changesets", body: `{"title":"Fix retries","stacked_on_changeset_id":"parent"}`, reply: `{"data":{"id":"change"}}`, output: `{"data":{"id":"change"}}`},
		{name: "materialize", args: []string{"materialize", "--changeset", "change"}, method: "POST", path: "/sessions/session/changesets/change/materialize", reply: `{"data":{"ready":true}}`, output: `{"data":{"ready":true}}`},
		{name: "verify", args: []string{"verify"}, method: "POST", path: "/sessions/session/changesets/verify", reply: `{"data":{"valid":true}}`, output: `{"data":{"valid":true}}`},
		{name: "publish", args: []string{"publish", "--changeset", "change"}, method: "POST", path: "/sessions/session/changesets/change/publish", reply: `{"data":{"published":true}}`, output: `{"data":{"published":true}}`},
		{name: "internal publish", args: []string{"publish", "--changeset", "change"}, internal: true, method: "POST", path: "/session/pr", body: `{"session_id":"session","changeset_id":"change","trigger_kind":"explicit_action"}`, reply: `{"status":"pr_queued","session_id":"session"}`, output: `{"status":"pr_queued","session_id":"session"}`},
		{name: "internal publish reviewer metadata", args: []string{"publish", "--changeset", "primary", "--team-reviewers", "Acme/TEAM,other"}, internal: true, method: "POST", path: "/session/pr", body: `{"session_id":"session","changeset_id":"primary","team_reviewers":["Acme/TEAM","other"],"trigger_kind":"explicit_action"}`, reply: `{"status":"awaiting_automatic_publication","session_id":"session"}`, output: `{"status":"awaiting_automatic_publication","session_id":"session"}`},
		{name: "publish stack", args: []string{"publish-stack"}, method: "POST", path: "/sessions/session/changesets/publish-stack", reply: `{"data":{"published":true}}`, output: `{"data":{"published":true}}`},
		{name: "restack", args: []string{"restack", "--from", "parent"}, method: "POST", path: "/sessions/session/changesets/parent/restack-descendants", reply: `{"data":{"restacked":true}}`, output: `{"data":{"restacked":true}}`},
		{name: "explicit session overrides environment", args: []string{"list", "--session-id", "other"}, method: "GET", path: "/sessions/other/changesets", reply: `{"data":[]}`, output: `{"data":[]}`},
		{name: "backend unavailable", args: []string{"publish-stack"}, method: "POST", path: "/sessions/session/changesets/publish-stack", status: 503, output: "HTTP 503"},
		{name: "usage", output: "Usage:"}, {name: "unknown action", args: []string{"unknown"}, output: "unknown changesets action"},
		{name: "diff missing changeset", args: []string{"diff"}, output: "--changeset is required"},
		{name: "materialize missing changeset", args: []string{"materialize"}, output: "--changeset is required"},
		{name: "publish missing changeset", args: []string{"publish"}, output: "--changeset is required"},
		{name: "import missing changeset", args: []string{"import-remote"}, output: "--changeset is required"},
		{name: "create missing title", args: []string{"create", "--title", " "}, output: "--title is required"},
		{name: "restack missing parent", args: []string{"restack"}, output: "--from is required"},
		{name: "missing session", args: []string{"list"}, output: "--session-id is required"},
		{name: "missing credentials", args: []string{"list"}, output: "credentials are unavailable"},
		{name: "public publish reviewers unsupported", args: []string{"publish", "--changeset", "change", "--team-reviewers", "acme/team"}, output: "--team-reviewers requires sandbox publication"},
		{name: "reviewers unsupported for other action", args: []string{"publish-stack", "--team-reviewers", "acme/team"}, internal: true, output: "--team-reviewers is supported only for publish"},
		{name: "unknown publish flag", args: []string{"publish", "--changeset", "change", "--team-reviewer", "acme/team"}, internal: true, output: `unknown flag "--team-reviewer"`},
		{name: "unexpected positional argument", args: []string{"list", "extra"}, output: `unknown flag "extra"`},
		{name: "missing session flag value", args: []string{"list", "--session-id"}, output: "--session-id requires a value"},
		{name: "missing changeset flag value", args: []string{"publish", "--changeset"}, output: "--changeset requires a value"},
		{name: "missing title flag value", args: []string{"create", "--title"}, output: "--title requires a value"},
		{name: "missing parent flag value", args: []string{"create", "--stacked-on"}, output: "--stacked-on requires a value"},
		{name: "missing from flag value", args: []string{"restack", "--from"}, output: "--from requires a value"},
		{name: "missing head flag value", args: []string{"import-remote", "--changeset", "change", "--head-sha"}, output: "--head-sha requires a value"},
		{name: "missing reviewers flag value", args: []string{"publish", "--changeset", "change", "--team-reviewers"}, internal: true, output: "--team-reviewers requires a value"},
		{name: "empty reviewers flag value", args: []string{"publish", "--changeset", "change", "--team-reviewers", " "}, internal: true, output: "--team-reviewers requires a value"},
		{name: "flag cannot consume another flag", args: []string{"publish", "--changeset", "--team-reviewers", "acme/team"}, internal: true, output: "--changeset requires a value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv(internalapi.CodingSessionIDEnvVar, "session")
			t.Setenv("143_CHANGESET_ID", "")
			t.Setenv("INTERNAL_API_TOKEN", "")
			t.Setenv(sandboxauth.SocketEnvVar, "")
			if tt.name == "current explicitly selected" {
				t.Setenv("143_CHANGESET_ID", "selected")
			}
			if tt.name == "missing session" {
				t.Setenv(internalapi.CodingSessionIDEnvVar, "")
			}
			var observed []struct{ method, path, auth, body string }
			var mu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, e := io.ReadAll(r.Body)
				if e != nil {
					t.Error(e)
					w.WriteHeader(500)
					return
				}
				mu.Lock()
				observed = append(observed, struct{ method, path, auth, body string }{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), string(b)})
				mu.Unlock()
				if tt.status != 0 {
					w.WriteHeader(tt.status)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if _, err := io.WriteString(w, tt.reply); err != nil {
					t.Error(err)
				}
			}))
			defer closeCLIHTTPTestServer(t, server)
			if tt.name != "missing credentials" {
				require.NoError(t, SaveConfig(Config{ServerURL: server.URL, Token: "public-token"}), "save isolated CLI config")
			}
			prefix := "/api/v1"
			auth := "Bearer public-token"
			if tt.internal {
				t.Setenv("INTERNAL_API_URL", server.URL)
				t.Setenv("INTERNAL_API_TOKEN", "session-token")
				prefix += "/internal"
				auth = "Bearer session-token"
			}
			var stdout, stderr bytes.Buffer
			code := runChangesets(tt.args, &stdout, &stderr)
			if tt.method == "" || tt.status != 0 {
				require.Equal(t, 1, code, "invalid or failed changeset actions must fail")
				require.Contains(t, stderr.String(), tt.output, "failure should explain required action")
			} else {
				require.Zero(t, code, "valid changeset action should succeed")
				require.JSONEq(t, tt.output, stdout.String(), "command must return exact requested changeset or response")
			}
			mu.Lock()
			requests := append([]struct{ method, path, auth, body string }(nil), observed...)
			mu.Unlock()
			if tt.method == "" {
				require.Empty(t, requests, "validation must not perform changeset actions")
				return
			}
			require.Len(t, requests, 1, "command should send exactly one action")
			require.Equal(t, tt.method, requests[0].method, "changeset action should use correct method")
			require.Equal(t, prefix+tt.path, requests[0].path, "sandbox and public routes must remain separate")
			require.Equal(t, auth, requests[0].auth, "sandbox should use session credential and CLI should use public credential")
			if tt.body != "" {
				require.JSONEq(t, tt.body, requests[0].body, "request must preserve changeset parent and title")
			} else {
				require.Empty(t, requests[0].body, "bodyless action must not submit unintended fields")
			}
		})
	}
}

func TestChangesetCommandMalformedConfig(t *testing.T) {
	// Credential config location is process-wide; this test is serial.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(internalapi.CodingSessionIDEnvVar, "session")
	t.Setenv("INTERNAL_API_TOKEN", "")
	t.Setenv(sandboxauth.SocketEnvVar, "")
	path, err := ConfigPath()
	require.NoError(t, err, "locate isolated config")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700), "create private config directory")
	require.NoError(t, os.WriteFile(path, []byte("{"), 0600), "write malformed config fixture")
	var stdout, stderr bytes.Buffer
	code := runChangesets([]string{"list"}, &stdout, &stderr)
	require.Equal(t, 1, code, "broken credentials must fail before sending a request")
	require.Empty(t, stdout.String(), "failed command must not print a successful changeset result")
	require.Contains(t, stderr.String(), "credentials are unavailable", "command should explain config failure")
	var response json.RawMessage
	require.Error(t, json.Unmarshal(stdout.Bytes(), &response), "failed command must not manufacture a valid JSON result")
}
