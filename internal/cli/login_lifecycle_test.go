package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/services/sandboxauth"
	"github.com/stretchr/testify/require"
)

// The CLI config uses process environment; these cases intentionally serialize.
// The callback and exchange both use disposable loopback servers.
func TestCLILoginLifecycle(t *testing.T) {
	tests := []struct {
		name, callbackError, exchangeError string
		githubName, organization           bool
		expectedCode                       int
		expectedMessage                    string
	}{
		{name: "GitHub identity and org", githubName: true, organization: true, expectedMessage: "Logged in as @tester (Example)"},
		{name: "plain identity without org", expectedMessage: "Logged in as Tester"},
		{name: "callback error preserves credential", callbackError: "access_denied", expectedCode: 1, expectedMessage: "login failed: access_denied"},
		{name: "token exchange failure preserves credential", exchangeError: "failed", expectedCode: 1, expectedMessage: "token exchange failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("INTERNAL_API_TOKEN", "")
			t.Setenv(sandboxauth.SocketEnvVar, "")
			var mu sync.Mutex
			var exchangeVerifier string
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requests = append(requests, r.Method+" "+r.URL.Path)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/auth/cli/exchange" {
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						http.Error(w, "bad request", 400)
						return
					}
					mu.Lock()
					exchangeVerifier = body["verifier"]
					mu.Unlock()
					if body["code"] != "single-use-code" || r.Header.Get("Authorization") != "" {
						http.Error(w, "exchange binding failed", 400)
						return
					}
					if tt.exchangeError != "" {
						http.Error(w, "unavailable", 503)
						return
					}
					response := exchangeResponse{Token: "new-credential", TokenID: "new-token", User: exchangeUser{Name: "Tester"}}
					if tt.githubName {
						login := "tester"
						response.User.GitHubLogin = &login
					}
					if tt.organization {
						response.Org = &exchangeOrg{ID: "org-new", Name: "Example"}
					}
					if err := json.NewEncoder(w).Encode(map[string]any{"data": response}); err != nil {
						http.Error(w, "encoding failed", 500)
					}
					return
				}
				if r.URL.Path == "/api/v1/auth/cli-tokens" {
					if r.Header.Get("Authorization") != "Bearer new-credential" {
						http.Error(w, "wrong credential", 401)
						return
					}
					fmt.Fprint(w, `{"data":[]}`)
					return
				}
				http.NotFound(w, r)
			}))
			defer server.Close()
			seed := Config{Version: 1, ServerURL: server.URL, Token: "old-credential", OrgID: "org-old", PendingJoinToken: "pending-join"}
			require.NoError(t, SaveConfig(seed), "seed prior credential and pending org invitation")
			printed := make(chan string, 1)
			var output bytes.Buffer
			writer := &loginURLWriter{output: &output, urls: printed}
			callbackDone := make(chan error, 1)
			var challenge, join string
			go func() {
				select {
				case raw := <-printed:
					parsed, err := url.Parse(raw)
					if err != nil {
						callbackDone <- err
						return
					}
					challenge = parsed.Query().Get("challenge")
					join = parsed.Query().Get("join")
					params := url.Values{"code": {"single-use-code"}}
					if tt.callbackError != "" {
						params = url.Values{"error": {tt.callbackError}}
					}
					client := &http.Client{Timeout: 5 * time.Second}
					response, err := client.Get("http://127.0.0.1:" + parsed.Query().Get("port") + "/callback?" + params.Encode())
					if err == nil {
						err = response.Body.Close()
					}
					callbackDone <- err
				case <-time.After(5 * time.Second):
					callbackDone <- fmt.Errorf("CLI did not expose callback URL")
				}
			}()
			var stderr bytes.Buffer
			code := runLogin([]string{"--no-browser", "--server", server.URL + "/"}, writer, &stderr)
			require.NoError(t, <-callbackDone, "loopback callback must finish without touching an external browser")
			require.Equal(t, tt.expectedCode, code, "login must preserve success or exact failure status")
			require.Contains(t, output.String()+stderr.String(), tt.expectedMessage, "login must display actionable identity or error")
			require.Equal(t, "pending-join", join, "pending invitation must be bound into authorization URL")
			saved, err := LoadConfig()
			require.NoError(t, err, "read credential after login")
			if code != 0 {
				require.Equal(t, seed, saved, "failed callback or exchange must preserve entire prior config")
			} else {
				expected := Config{Version: 1, ServerURL: server.URL, Token: "new-credential", OrgID: "org-old"}
				if tt.organization {
					expected.OrgID = "org-new"
				}
				require.Equal(t, expected, saved, "successful exchange must atomically replace token, update org and retire pending join")
			}
			mu.Lock()
			verifier := exchangeVerifier
			actualRequests := append([]string(nil), requests...)
			mu.Unlock()
			var wantRequests []string
			if tt.callbackError == "" {
				wantRequests = []string{"POST /api/v1/auth/cli/exchange"}
				sum := sha256.Sum256([]byte(verifier))
				require.Equal(t, hex.EncodeToString(sum[:]), challenge, "redeemed code must be bound to exact browser PKCE challenge")
				require.Len(t, verifier, 64, "private verifier must contain all 32 bytes of entropy")
			}
			if code == 0 {
				wantRequests = append(wantRequests, "GET /api/v1/auth/cli-tokens")
			}
			require.Equal(t, wantRequests, actualRequests, "login must exchange once and retire devices only after successful persistence")
		})
	}
}

type loginURLWriter struct {
	output *bytes.Buffer
	urls   chan<- string
}

func (w *loginURLWriter) Write(p []byte) (int, error) {
	n, err := w.output.Write(p)
	for _, line := range strings.Split(string(p), "\n") {
		value := strings.TrimSpace(line)
		if strings.Contains(value, "/api/v1/auth/cli/start?") {
			w.urls <- value
		}
	}
	return n, err
}

func TestCLILoginFlagValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    []string
		code    int
		message string
	}{{"help", []string{"--help"}, 0, "Usage: 143-tools login"}, {"missing server", []string{"--server"}, 1, "--server requires a value"}, {"missing invitation", []string{"--join"}, 1, "--join requires a value"}, {"unknown flag", []string{"--unknown"}, 1, "unknown flag"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var out, err bytes.Buffer
			require.Equal(t, tt.code, runLogin(tt.args, &out, &err), "invalid flags must return before starting network or reading config")
			require.Contains(t, out.String()+err.String(), tt.message, "validation must identify exact invalid option")
		})
	}
}
