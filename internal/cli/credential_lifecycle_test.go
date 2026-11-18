package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/assembledhq/143/internal/services/sandboxauth"
	"github.com/stretchr/testify/require"
)

func TestCLICredentialLifecycle(t *testing.T) {
	// ConfigPath reads process environment. These cases use t.Setenv and must
	// remain serial; all network traffic and credential files are isolated.
	tests := []struct {
		name, command                                              string
		args                                                       []string
		seed, malformed, serverChanged, listFailure, revokeFailure bool
		expectedCode                                               int
		expectedRequests                                           []string
		expectedOutput                                             string
		tokenSurvives                                              bool
	}{
		{name: "setup from scratch", command: "setup", expectedCode: 0, expectedOutput: "Configured server"},
		{name: "setup preserves same-server credential", command: "setup", seed: true, tokenSurvives: true, expectedOutput: "Configured server"},
		{name: "changing server clears credential", command: "setup", seed: true, serverChanged: true, expectedOutput: "Configured server"},
		{name: "setup requires value", command: "setup", args: []string{"--server"}, expectedCode: 1, expectedOutput: "--server requires a value"},
		{name: "setup requires server", command: "setup", args: []string{"--join", "join-token"}, expectedCode: 1, expectedOutput: "--server is required"},
		{name: "setup rejects unknown flags", command: "setup", args: []string{"--unknown"}, expectedCode: 1, expectedOutput: "unknown flag"},
		{name: "malformed config rejected", command: "setup", malformed: true, expectedCode: 1, expectedOutput: "parse"},
		{name: "logout without credential", command: "logout", expectedOutput: "Not logged in."},
		{name: "logout revokes matching token", command: "logout", seed: true, expectedRequests: []string{"GET /api/v1/auth/cli-tokens", "DELETE /api/v1/auth/cli-tokens/current"}, expectedOutput: "Logged out."},
		{name: "logout clears local token after failed revocation", command: "logout", seed: true, revokeFailure: true, expectedRequests: []string{"GET /api/v1/auth/cli-tokens", "DELETE /api/v1/auth/cli-tokens/current"}, expectedOutput: "could not revoke token server-side"},
		{name: "logout clears local token after failed lookup", command: "logout", seed: true, listFailure: true, expectedRequests: []string{"GET /api/v1/auth/cli-tokens"}, expectedOutput: "could not revoke token server-side"},
		{name: "local identity does not send token", command: "whoami", seed: true, args: []string{"--local"}, tokenSurvives: true, expectedOutput: "token 143u_abcdefgh..."},
		{name: "whoami needs credential", command: "whoami", expectedCode: 1, expectedOutput: "Not logged in"},
		{name: "remote identity", command: "whoami", seed: true, tokenSurvives: true, expectedRequests: []string{"GET /api/v1/auth/me", "GET /api/v1/auth/memberships", "GET /api/v1/cli/version"}, expectedOutput: "Org:     Example (member)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", root)
			t.Setenv("INTERNAL_API_TOKEN", "")
			t.Setenv(sandboxauth.SocketEnvVar, "")
			token := "143u_abcdefghijklmnopqrstuvwxyz"
			var mu sync.Mutex
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requests = append(requests, r.Method+" "+r.URL.Path)
				mu.Unlock()
				require.Equal(t, "Bearer "+token, r.Header.Get("Authorization"), "credential commands must authenticate only to configured server")
				w.Header().Set("Content-Type", "application/json")
				switch r.Method + " " + r.URL.Path {
				case "GET /api/v1/auth/cli-tokens":
					if tt.listFailure {
						w.WriteHeader(503)
						return
					}
					_, err := w.Write([]byte(`{"data":[{"id":"other","token_prefix":"143u_different"},{"id":"current","token_prefix":"143u_abcdefgh"}]}`))
					require.NoError(t, err, "write isolated token inventory")
				case "DELETE /api/v1/auth/cli-tokens/current":
					if tt.revokeFailure {
						w.WriteHeader(503)
					} else {
						w.WriteHeader(204)
					}
				case "GET /api/v1/auth/me":
					_, err := w.Write([]byte(`{"data":{"email":"person@example.test","name":"Person","github_login":"person"}}`))
					require.NoError(t, err, "write isolated identity")
				case "GET /api/v1/auth/memberships":
					_, err := w.Write([]byte(`{"data":{"active_org_id":"org","active_role":"member","memberships":[{"org_id":"other","org_name":"Other"},{"org_id":"org","org_name":"Example"}]}}`))
					require.NoError(t, err, "write isolated memberships")
				case "GET /api/v1/cli/version":
					_, err := w.Write([]byte(`{"data":{"version":"new-release"}}`))
					require.NoError(t, err, "write test version")
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer closeCLIHTTPTestServer(t, server)
			if tt.seed {
				require.NoError(t, SaveConfig(Config{ServerURL: server.URL, Token: token, OrgID: "org"}), "seed isolated config")
			}
			if tt.malformed {
				path, err := ConfigPath()
				require.NoError(t, err, "resolve isolated config path")
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700), "create test config directory")
				require.NoError(t, os.WriteFile(path, []byte("{"), 0600), "write malformed fixture")
			}
			args := tt.args
			if tt.command == "setup" && args == nil {
				target := server.URL
				if tt.serverChanged {
					target = "https://different.example.test"
				}
				args = []string{"--server", target + "/", "--join", "pending-join"}
			}
			var stdout, stderr bytes.Buffer
			handled, code := HandleSubcommand(append([]string{tt.command}, args...), &stdout, &stderr)
			require.True(t, handled, "credential commands must be handled before tool dispatch")
			require.Equal(t, tt.expectedCode, code, "credential command should preserve success and validation outcomes")
			require.Contains(t, stdout.String()+stderr.String(), tt.expectedOutput, "command should explain credential outcome")
			require.NotContains(t, stdout.String()+stderr.String(), token, "commands must not print plaintext credential")
			mu.Lock()
			observed := append([]string(nil), requests...)
			mu.Unlock()
			require.Equal(t, tt.expectedRequests, observed, "credential lifecycle should send only intended requests")
			if !tt.malformed {
				cfg, err := LoadConfig()
				require.NoError(t, err, "read resulting isolated config")
				expectedToken := ""
				if tt.tokenSurvives {
					expectedToken = token
				}
				require.Equal(t, expectedToken, cfg.Token, "setup must preserve only same-server credentials and logout must clear them")
				if tt.command == "setup" && tt.expectedCode == 0 {
					require.Equal(t, "pending-join", cfg.PendingJoinToken, "setup should retain install join token until login")
				}
				path, err := ConfigPath()
				require.NoError(t, err, "resolve result file")
				if info, err := os.Stat(path); err == nil {
					require.Equal(t, os.FileMode(0600), info.Mode().Perm(), "credential config must stay private")
				}
			}
		})
	}
}

func TestCLIExchangeCodeAndDeviceRetirement(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		failureStatus int
		emptyToken    bool
		expectedError string
	}{
		{name: "successful exchange"}, {name: "missing token", emptyToken: true, expectedError: "server returned no token"}, {name: "expired code", failureStatus: 401, expectedError: "server returned HTTP 401"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, http.MethodPost, r.Method, "code exchange must use POST")
				require.Equal(t, "/api/v1/auth/cli/exchange", r.URL.Path, "exchange must use authentication endpoint")
				require.Empty(t, r.Header.Get("Authorization"), "exchange must not send a stale bearer credential")
				var body map[string]string
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body), "decode exchange request")
				require.Equal(t, map[string]string{"code": "one-time-code", "verifier": "private-verifier"}, body, "exchange must bind authorization code to private PKCE verifier")
				if tt.failureStatus != 0 {
					w.WriteHeader(tt.failureStatus)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				token := "new-token"
				if tt.emptyToken {
					token = ""
				}
				_, err := fmt.Fprintf(w, `{"data":{"token":%q,"token_id":"new-id","token_prefix":"new-prefix","user":{"id":"user","name":"Person","email":"person@example.test"},"org":{"id":"org","name":"Example"}}}`, token)
				require.NoError(t, err, "write exchange fixture")
			}))
			defer closeCLIHTTPTestServer(t, server)
			response, err := exchangeCode(context.Background(), server.URL, "one-time-code", "private-verifier")
			if tt.expectedError != "" {
				require.ErrorContains(t, err, tt.expectedError, "invalid exchange must not create usable credentials")
				require.Nil(t, response, "failed exchange should not return a token")
			} else {
				require.NoError(t, err, "valid exchange should return credential")
				require.Equal(t, &exchangeResponse{Token: "new-token", TokenID: "new-id", TokenPrefix: "new-prefix", User: exchangeUser{ID: "user", Name: "Person", Email: "person@example.test"}, Org: &exchangeOrg{ID: "org", Name: "Example"}}, response, "exchange should preserve exact authenticated identity")
			}
		})
	}
	t.Run("retire only same-device older tokens", func(t *testing.T) {
		t.Parallel()
		var requests []string
		var mu sync.Mutex
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			requests = append(requests, r.Method+" "+r.URL.Path)
			mu.Unlock()
			require.Equal(t, "Bearer new-token", r.Header.Get("Authorization"), "retirement must use the newly minted token")
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/json")
				_, err := w.Write([]byte(`{"data":[{"id":"keep","device_name":"Laptop"},{"id":"old","device_name":"Laptop"},{"id":"other","device_name":"Desktop"}]}`))
				require.NoError(t, err, "write device inventory")
			} else {
				w.WriteHeader(204)
			}
		}))
		defer closeCLIHTTPTestServer(t, server)
		var stderr bytes.Buffer
		revokeStaleDeviceTokens(context.Background(), Config{ServerURL: server.URL, Token: "new-token"}, "keep", "Laptop", &stderr)
		mu.Lock()
		observed := append([]string(nil), requests...)
		mu.Unlock()
		require.Equal(t, []string{"GET /api/v1/auth/cli-tokens", "DELETE /api/v1/auth/cli-tokens/old"}, observed, "retirement must preserve new token and other-device tokens")
		require.Empty(t, strings.TrimSpace(stderr.String()), "successful retirement should not warn")
	})
}
