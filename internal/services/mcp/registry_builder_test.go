package mcp

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/sandboxauth"
)

// shortSocketDir mirrors the helper in sandboxauth tests: AF_UNIX paths max
// out around 104 bytes on macOS, so we sit directly under /tmp to keep the
// path budget for UUID-style filenames.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "143reg-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func clearGitHubEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITHUB_REPO_OWNER", "")
	t.Setenv("GITHUB_REPO_NAME", "")
	t.Setenv(sandboxauth.SocketEnvVar, "")
}

func TestBuildRegistryFromEnv_GitHub_LegacyToken(t *testing.T) {
	clearGitHubEnv(t)
	t.Setenv("GITHUB_TOKEN", "tok-123")
	t.Setenv("GITHUB_REPO_OWNER", "octocat")
	t.Setenv("GITHUB_REPO_NAME", "hello-world")

	reg := BuildRegistryFromEnv(io.Discard)

	src, err := reg.CodeReviewSource("github")
	require.NoError(t, err, "GITHUB_TOKEN + owner/repo should register the github source")
	require.Equal(t, "github", src.Name())
}

func TestBuildRegistryFromEnv_GitHub_SocketFallback(t *testing.T) {
	clearGitHubEnv(t)
	sockPath := filepath.Join(shortSocketDir(t), "sock")
	t.Setenv(sandboxauth.SocketEnvVar, sockPath)
	t.Setenv("GITHUB_REPO_OWNER", "octocat")
	t.Setenv("GITHUB_REPO_NAME", "hello-world")

	reg := BuildRegistryFromEnv(io.Discard)

	src, err := reg.CodeReviewSource("github")
	require.NoError(t, err, "_143_AUTH_SOCK + owner/repo should register the github source even without GITHUB_TOKEN")
	require.Equal(t, "github", src.Name())
}

func TestBuildRegistryFromEnv_CircleCI(t *testing.T) {
	t.Setenv("CIRCLECI_TOKEN", "cci-tok")
	t.Setenv("CIRCLECI_PROJECT_SLUG", "gh/octocat/hello")

	reg := BuildRegistryFromEnv(io.Discard)

	p, err := reg.CITestInsightsProvider("circleci")
	require.NoError(t, err, "CIRCLECI_TOKEN + slug should register the circleci provider")
	require.Equal(t, "circleci", p.Name())
}

func TestBuildRegistryFromEnv_CircleCI_MissingSlugSkips(t *testing.T) {
	t.Setenv("CIRCLECI_TOKEN", "cci-tok")
	t.Setenv("CIRCLECI_PROJECT_SLUG", "")

	reg := BuildRegistryFromEnv(io.Discard)

	_, err := reg.CITestInsightsProvider("circleci")
	require.Error(t, err, "without a project slug, the CLI surface would 404 — provider must not register")
}

func TestBuildRegistryFromEnv_PagerDutyWriteToolsFollowWritebackSetting(t *testing.T) {
	// Environment variables are process-global, and testing.T forbids Setenv
	// in parallel tests. Keep this one serial so each case owns the registry
	// input env.

	tests := []struct {
		name             string
		writebackEnabled string
		expectWriteTools bool
	}{
		{name: "hidden by default", expectWriteTools: false},
		{name: "visible when enabled", writebackEnabled: "true", expectWriteTools: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PAGERDUTY_ACCESS_TOKEN", "pd-token")
			t.Setenv("PAGERDUTY_WRITEBACK_ENABLED", tt.writebackEnabled)

			tools := NewToolRegistry(BuildRegistryFromEnv(io.Discard)).ListTools()
			names := make([]string, 0, len(tools))
			for _, tool := range tools {
				names = append(names, tool.Name)
			}

			require.Contains(t, names, "pagerduty_list_incidents", "PagerDuty read tools should be available with an access token")
			if tt.expectWriteTools {
				require.Contains(t, names, "pagerduty_add_note", "PagerDuty note write tool should be visible when writeback is enabled")
				require.Contains(t, names, "pagerduty_create_status_update", "PagerDuty status write tool should be visible when writeback is enabled")
				return
			}
			require.NotContains(t, names, "pagerduty_add_note", "PagerDuty note write tool should be hidden when writeback is disabled")
			require.NotContains(t, names, "pagerduty_create_status_update", "PagerDuty status write tool should be hidden when writeback is disabled")
		})
	}
}

func TestBuildRegistryFromEnv_GitHub_NoCredsSkipsSource(t *testing.T) {
	clearGitHubEnv(t)
	// Owner/repo present but neither credential path — the source must NOT
	// register, otherwise agents see github_* tools in the skills doc that
	// fail at call time.
	t.Setenv("GITHUB_REPO_OWNER", "octocat")
	t.Setenv("GITHUB_REPO_NAME", "hello-world")

	reg := BuildRegistryFromEnv(io.Discard)

	_, err := reg.CodeReviewSource("github")
	require.Error(t, err, "without creds, github must not register")
}
