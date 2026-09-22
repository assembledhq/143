package mcp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAutomationActionCLI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, mode, status string
		want               int
		path, method       string
	}{
		{"file delivered", "file", "delivered", 0, "/api/v1/internal/automation/actions", "POST"},
		{"resume partial", "resume", "partial", 1, "/api/v1/internal/automation/actions/resume", "POST"},
		{"resume unknown", "resume", "needs_attention", 1, "/api/v1/internal/automation/actions/resume", "POST"},
		{"status", "status", "partial", 0, "/api/v1/internal/automation/actions", "GET"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var gotPath, gotMethod, gotBody string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotMethod = r.URL.Path, r.Method
				raw, err := io.ReadAll(r.Body)
				require.NoError(t, err, "read request")
				gotBody = string(raw)
				require.Equal(t, "Bearer token", r.Header.Get("Authorization"), "forward scoped internal token")
				_, err = fmt.Fprintf(w, `{"data":{"status":%q,"actions":[]}}`, tt.status)
				require.NoError(t, err, "return receipts")
			}))
			defer server.Close()
			args := []string{"automation", "execute-action", "--resume", "--operation_key", "daily", "--action_key", "notify"}
			if tt.mode == "status" {
				args = []string{"automation", "action-status", "--operation_key", "daily"}
			}
			if tt.mode == "file" {
				file := filepath.Join(t.TempDir(), "verdict.json")
				require.NoError(t, os.WriteFile(file, []byte(`{"reasoning":"multi\nline"}`), 0600), "write local verdict")
				args = []string{"automation", "execute-action", "--file", file}
			}
			source := NewInternalMetaToolSource(staticToolSource{}, "token", server.URL)
			var stdout, stderr bytes.Buffer
			code := RunCLI(context.Background(), source, args, &stdout, &stderr)
			require.Equal(t, tt.want, code, "partial delivery must not exit successfully")
			require.Equal(t, tt.path, gotPath, "use internal route")
			require.Equal(t, tt.method, gotMethod, "use fixed method")
			require.JSONEq(t, fmt.Sprintf(`{"data":{"status":%q,"actions":[]}}`, tt.status), stdout.String(), "preserve parseable receipts even when incomplete")
			if tt.mode == "file" {
				require.JSONEq(t, `{"reasoning":"multi\nline"}`, gotBody, "load exact file content without shell quoting")
			}
			if tt.mode == "resume" {
				require.JSONEq(t, `{"operation_key":"daily","action_key":"notify"}`, gotBody, "resume sends no replacement content")
			}
		})
	}
}
