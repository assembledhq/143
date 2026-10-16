package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func newReviewToolRepairFixture(t *testing.T) (reviewEvidenceGitFixture, *Sandbox, string, string) {
	t.Helper()
	config, err := json.Marshal(repoconfig.Config{
		Dependencies: map[string]string{"golangci-lint": "2.8.0"},
		Review: &repoconfig.ReviewSection{
			Commands:       []string{"mkdir -p .143/cache/go; printf 'package dep\\n' > .143/cache/go/api.go; printf 'setup\\n' >> executions.log"},
			VerifyCommands: []string{"printf 'verify\\n' >> permit; if test -f verifier-dirty; then printf dirty > changed.go; exit 31; fi; test -f .143/cache/go/api.go"},
			Evidence:       []repoconfig.ReviewEvidenceEntry{{Path: ".143/cache/go", Description: "Pinned Go source", Category: models.CodeReviewEvidenceGoDependencies}},
		},
	})
	require.NoError(t, err, "the tool repair fixture must declare a real registered pin and reusable source recipe")
	f := newReviewEvidenceGitFixtureWithConfig(t, string(config), false)
	root := t.TempDir()
	home, bin := filepath.Join(root, "home"), filepath.Join(root, "bin")
	require.NoError(t, os.Mkdir(home, 0o700), "the sandbox tool installation must use an isolated home")
	require.NoError(t, os.Mkdir(bin, 0o700), "the fixture download transport must have an isolated executable directory")
	var archive bytes.Buffer
	compressed := gzip.NewWriter(&archive)
	writer := tar.NewWriter(compressed)
	body := []byte("#!/bin/sh\nprintf 'golangci-lint has version 2.8.0 fixture\\n'\n")
	require.NoError(t, writer.WriteHeader(&tar.Header{Name: "golangci-lint-2.8.0-" + runtime.GOOS + "-" + runtime.GOARCH + "/golangci-lint", Mode: 0o755, Size: int64(len(body))}), "the local archive must match the existing install recipe's platform path")
	_, err = writer.Write(body)
	require.NoError(t, err, "the local archive must provide only its synthetic version executable")
	require.NoError(t, writer.Close(), "the synthetic archive must finish its source payload")
	require.NoError(t, compressed.Close(), "the synthetic archive must finish compression")
	archivePath, eventsPath := filepath.Join(root, "tool.tar.gz"), filepath.Join(root, "installs.log")
	require.NoError(t, os.WriteFile(archivePath, archive.Bytes(), 0o600), "the fixture must retain a local archive without network dependencies")
	transport := `#!/bin/sh
set -eu
test -z "${GITHUB_TOKEN:-}${GH_TOKEN:-}${GIT_CONFIG_COUNT:-}"
printf 'install\n' >> "$REVIEW_TEST_TOOL_EVENTS"
if test "${REVIEW_TEST_TOOL_FAIL:-}" = 1; then exit 42; fi
destination=
while test "$#" -gt 0; do
    if test "$1" = -o; then destination=$2; shift 2; else shift; fi
done
test -n "$destination"
/bin/cp "$REVIEW_TEST_TOOL_ARCHIVE" "$destination"
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "curl"), []byte(transport), 0o700), "only the download transport should be replaced while the registered installer runs unchanged")
	sandbox := f.sandbox()
	sandbox.HomeDir = home
	sandbox.Env = map[string]string{"HOME": home, "PATH": bin + ":/usr/bin:/bin", "REVIEW_TEST_TOOL_ARCHIVE": archivePath, "REVIEW_TEST_TOOL_EVENTS": eventsPath}
	return f, sandbox, filepath.Join(home, ".local/bin/golangci-lint"), eventsPath
}

func TestReviewToolRepairPreservesValidSourceRecipes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, invalidate                           string
		warm, missingTool, expired, installerFails bool
		wantErr, sameReceipt                       bool
		installs, setups, verifications            string
	}{
		{name: "fresh missing tool", installs: "install\n", setups: "setup\n", verifications: "verify\n"},
		{name: "healthy warm receipt", warm: true, sameReceipt: true, installs: "install\n", setups: "setup\n", verifications: "verify\nverify\n"},
		{name: "warm missing tool", warm: true, missingTool: true, sameReceipt: true, installs: "install\ninstall\n", setups: "setup\n", verifications: "verify\nverify\n"},
		{name: "warm incorrect tool pin", warm: true, invalidate: "tool_version", sameReceipt: true, installs: "install\ninstall\n", setups: "setup\n", verifications: "verify\nverify\n"},
		{name: "expired missing tool", warm: true, missingTool: true, expired: true, wantErr: true, sameReceipt: true, installs: "install\n", setups: "setup\n", verifications: "verify\n"},
		{name: "failed tool repair", warm: true, missingTool: true, installerFails: true, wantErr: true, sameReceipt: true, installs: "install\ninstall\n", setups: "setup\n", verifications: "verify\n"},
		{name: "missing source and tool", warm: true, missingTool: true, invalidate: "source", installs: "install\ninstall\n", setups: "setup\nsetup\n", verifications: "verify\nverify\n"},
		{name: "changed source and tool", warm: true, missingTool: true, invalidate: "changed_source", installs: "install\ninstall\n", setups: "setup\nsetup\n", verifications: "verify\nverify\n"},
		{name: "stale receipt and missing tool", warm: true, missingTool: true, invalidate: "receipt", installs: "install\ninstall\n", setups: "setup\nsetup\n", verifications: "verify\nverify\n"},
		{name: "malformed receipt and missing tool", warm: true, missingTool: true, invalidate: "malformed", installs: "install\ninstall\n", setups: "setup\nsetup\n", verifications: "verify\nverify\n"},
		{name: "repaired tool verifier dirties source", warm: true, missingTool: true, invalidate: "verifier", wantErr: true, sameReceipt: true, installs: "install\ninstall\n", setups: "setup\n", verifications: "verify\nverify\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, sandbox, tool, installs := newReviewToolRepairFixture(t)
			o := f.orchestrator()
			require.NoError(t, o.hydrateCodeReviewTrees(context.Background(), f.session, sandbox, f.remote, "", zerolog.Nop()), "the independent tool cases must begin with complete real Git source evidence")
			tokens := &reviewConfigGitHubTokens{token: "fake-tool-repair-token"}
			o.github = tokens
			ctx := withCodeReviewPreparationBudget(context.Background())
			if tt.warm {
				require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(ctx, f.session, sandbox, zerolog.Nop()), "warm repair cases must first publish a valid receipt through the complete registered installer")
			}
			if tt.missingTool {
				require.NoError(t, os.Remove(tool), "the warm workspace must lose only its declared tool executable")
			}
			receipt := filepath.Join(f.workDir, ".git/143-review-evidence.json")
			switch tt.invalidate {
			case "tool_version":
				require.NoError(t, os.WriteFile(tool, []byte("#!/bin/sh\nprintf 'golangci-lint has version 2.7.0 fixture\\n'\n"), 0o755), "the installed tool must expose a stale pin without changing source evidence")
			case "changed_source":
				require.NoError(t, os.WriteFile(filepath.Join(f.workDir, ".143/cache/go/api.go"), []byte("package changed\n"), 0o600), "changed manifest metadata must require full preparation before tool-only repair")
			case "source":
				require.NoError(t, os.Remove(filepath.Join(f.workDir, ".143/cache/go/api.go")), "missing declared source must require complete recipe preparation")
			case "receipt":
				require.NoError(t, os.WriteFile(receipt, []byte(`{"revision":{"config_hash":"stale"}}`), 0o600), "the stale receipt must not authorize tool-only repair")
			case "malformed":
				require.NoError(t, os.WriteFile(receipt, []byte("{"), 0o600), "malformed readiness must not authorize tool-only repair")
			case "verifier":
				require.NoError(t, os.WriteFile(filepath.Join(f.workDir, "verifier-dirty"), nil, 0o600), "the warm verifier must expose the tracked-source failure path after tool repair")
			}
			var previous []byte
			var previousInfo os.FileInfo
			if tt.warm {
				var err error
				previous, err = os.ReadFile(receipt)
				require.NoError(t, err, "the existing receipt bytes must be captured before the target preparation")
				previousInfo, err = os.Stat(receipt)
				require.NoError(t, err, "the existing receipt identity must be captured before the target preparation")
			}
			if tt.expired {
				ctx = WithCodeReviewWorkspacePreparationDeadline(ctx, time.Now().Add(-time.Second))
			}
			if tt.installerFails {
				sandbox.Env["REVIEW_TEST_TOOL_FAIL"] = "1"
			}
			err := o.ensureCodeReviewWorkspaceEvidence(ctx, f.session, sandbox, zerolog.Nop())
			if tt.wantErr {
				require.Error(t, err, "expired, failed installation, and dirty-source repair must fail closed")
			} else {
				require.NoError(t, err, "healthy source evidence must support the expected bounded preparation or independent tool repair")
			}
			if tt.expired {
				require.ErrorIs(t, err, context.DeadlineExceeded, "an expired missing tool must retain the original preparation deadline identity")
			}
			for _, expected := range []struct{ path, value, reason string }{
				{path: installs, value: tt.installs, reason: "only the required tool installation attempts should run"},
				{path: filepath.Join(f.workDir, "executions.log"), value: tt.setups, reason: "missing tools alone must never rerun an already valid repository recipe"},
				{path: filepath.Join(f.workDir, "permit"), value: tt.verifications, reason: "repository verifiers must run only inside the live preparation budget"},
			} {
				actual, readErr := os.ReadFile(expected.path)
				require.NoError(t, readErr, "the target preparation must leave its isolated finite execution history readable")
				require.Equal(t, expected.value, string(actual), expected.reason)
			}
			require.Equal(t, 0, tokens.calls, "local tool repair and expired validation must never request repository authentication")
			if tt.warm {
				current, readErr := os.ReadFile(receipt)
				require.NoError(t, readErr, "the receipt must remain available after the target preparation")
				currentInfo, statErr := os.Stat(receipt)
				require.NoError(t, statErr, "the receipt identity must remain available after the target preparation")
				if tt.sameReceipt {
					require.Equal(t, previous, current, "tool repair and failed or expired validation must preserve the exact readiness receipt bytes")
					require.True(t, os.SameFile(previousInfo, currentInfo), "tool-only repair must preserve the existing receipt file instead of replacing it")
				} else {
					require.False(t, os.SameFile(previousInfo, currentInfo), "invalid cached evidence or receipt identity must be replaced only after complete preparation succeeds")
				}
			}
		})
	}
}
