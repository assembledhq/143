package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestReviewExpiredValidationChecksSourceAfterPlatformToolProbe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  bool
		wantErr bool
	}{
		{name: "healthy source passes final validation"},
		{name: "tool probe mutates tracked source", mutate: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, sandbox, tool, installs := newReviewToolRepairFixture(t)
			o := f.orchestrator()
			require.NoError(t, o.hydrateCodeReviewTrees(context.Background(), f.session, sandbox, f.remote, "", zerolog.Nop()), "the expired case must begin with complete pinned Git trees")
			require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, sandbox, zerolog.Nop()), "the normal preparation must publish the reusable source receipt before expiry")
			receiptPath := filepath.Join(f.workDir, ".git/143-review-evidence.json")
			receipt, err := os.ReadFile(receiptPath)
			require.NoError(t, err, "the original readiness receipt must be captured")
			receiptInfo, err := os.Stat(receiptPath)
			require.NoError(t, err, "the original receipt file identity must be captured")
			if tt.mutate {
				body := "#!/bin/sh\nprintf dirty > '" + shellEscapeSingleQuote(filepath.Join(f.workDir, "changed.go")) + "'\nprintf 'golangci-lint has version 2.8.0 fixture\\n'\n"
				require.NoError(t, os.WriteFile(tool, []byte(body), 0o755), "the platform tool probe must expose source mutation while still reporting the accepted version")
			}
			tokens := &reviewConfigGitHubTokens{token: "fake-expired-final-validation-token"}
			o.github = tokens
			ctx := WithCodeReviewWorkspacePreparationDeadline(context.Background(), time.Now().Add(-time.Second))
			err = o.ensureCodeReviewWorkspaceEvidence(ctx, f.session, sandbox, zerolog.Nop())
			if tt.wantErr {
				require.ErrorIs(t, err, context.DeadlineExceeded, "source changed during an expired tool probe must fail closed without attempting repair")
			} else {
				require.NoError(t, err, "the single final source check must accept complete unchanged evidence after expiry")
			}
			require.Equal(t, 0, tokens.calls, "expired validation must never request repository authentication")
			for _, expected := range []struct{ path, content string }{
				{installs, "install\n"},
				{filepath.Join(f.workDir, "executions.log"), "setup\n"},
				{filepath.Join(f.workDir, "permit"), "verify\n"},
			} {
				actual, readErr := os.ReadFile(expected.path)
				require.NoError(t, readErr, "the bounded preparation event history must remain readable")
				require.Equal(t, expected.content, string(actual), "expiry must not rerun installers, repository setup, or repository verifiers")
			}
			after, readErr := os.ReadFile(receiptPath)
			require.NoError(t, readErr, "the old readiness receipt must remain readable")
			afterInfo, statErr := os.Stat(receiptPath)
			require.NoError(t, statErr, "the old receipt file identity must remain readable")
			require.Equal(t, receipt, after, "expired verification must preserve the exact original receipt bytes")
			require.True(t, os.SameFile(receiptInfo, afterInfo), "expired verification must never replace the original receipt")
		})
	}
}
