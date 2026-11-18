package mcp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type automationActionTestTransport func(*http.Request) (*http.Response, error)

func (f automationActionTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestAutomationActionCLI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, mode, status string
		want               int
		path, method       string
		request            string
	}{
		{"file delivered", "file", "delivered", 0, "/api/v1/internal/automation/actions", "POST", `{"reasoning":"multi\nline"}`},
		{"file comment update", "file", "delivered", 0, "/api/v1/internal/automation/actions", "POST", `{"operation_key":"pr:42","action_key":"comment:revision-2","kind":"github_issue_comment_update","pr_number":42,"head_sha":"0123456789abcdef0123456789abcdef01234567","text":"Updated findings","source_action_id":"12345678-1234-1234-1234-123456789abc"}`},
		{"file inline review", "file", "delivered", 0, "/api/v1/internal/automation/actions", "POST", `{"operation_key":"pr:42","action_key":"review:head","kind":"github_pull_request_review","pr_number":42,"head_sha":"0123456789abcdef0123456789abcdef01234567","text":"One finding.","comments":[{"path":"ui/Card.tsx","line":12,"body":"Use the shared Card."}]}`},
		{"resume partial", "resume", "partial", 1, "/api/v1/internal/automation/actions/resume", "POST", ""},
		{"resume unknown", "resume", "needs_attention", 1, "/api/v1/internal/automation/actions/resume", "POST", ""},
		{"status", "status", "partial", 0, "/api/v1/internal/automation/actions", "GET", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var gotPath, gotMethod, gotBody string
			transport := automationActionTestTransport(func(r *http.Request) (*http.Response, error) {
				gotPath, gotMethod = r.URL.Path, r.Method
				if r.Body != nil {
					raw, err := io.ReadAll(r.Body)
					require.NoError(t, err, "read request")
					require.NoError(t, r.Body.Close(), "close request body")
					gotBody = string(raw)
				}
				require.Equal(t, "Bearer token", r.Header.Get("Authorization"), "forward scoped internal token")
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"data":{"status":%q,"actions":[]}}`, tt.status))),
				}, nil
			})
			args := []string{"automation", "execute-action", "--resume", "--operation_key", "daily", "--action_key", "notify"}
			if tt.mode == "status" {
				args = []string{"automation", "action-status", "--operation_key", "daily"}
			}
			if tt.mode == "file" {
				file := filepath.Join(t.TempDir(), "request.json")
				require.NoError(t, os.WriteFile(file, []byte(tt.request), 0600), "write local action request")
				args = []string{"automation", "execute-action", "--file", file}
			}
			source := NewInternalMetaToolSource(staticToolSource{}, "token", "https://example.test").(*internalMetaToolSource)
			source.client.Transport = transport
			var stdout, stderr bytes.Buffer
			code := RunCLI(context.Background(), source, args, &stdout, &stderr)
			require.Equal(t, tt.want, code, "partial delivery must not exit successfully")
			require.Equal(t, tt.path, gotPath, "use internal route")
			require.Equal(t, tt.method, gotMethod, "use fixed method")
			require.JSONEq(t, fmt.Sprintf(`{"data":{"status":%q,"actions":[]}}`, tt.status), stdout.String(), "preserve parseable receipts even when incomplete")
			if tt.mode == "file" {
				require.JSONEq(t, tt.request, gotBody, "load exact file content without shell quoting")
			}
			if tt.mode == "resume" {
				require.JSONEq(t, `{"operation_key":"daily","action_key":"notify"}`, gotBody, "resume sends no replacement content")
			}
		})
	}
}
