package agent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type reviewContainerBridgeProvider struct {
	managedReviewInputsProvider
	deadlines []time.Time
}

func (p *reviewContainerBridgeProvider) ExecWithStdin(ctx context.Context, sandbox *Sandbox, command string, input io.Reader, stdout, stderr io.Writer) (int, error) {
	deadline, _ := ctx.Deadline()
	p.mu.Lock()
	p.deadlines = append(p.deadlines, deadline)
	p.mu.Unlock()
	return p.managedReviewInputsProvider.ExecWithStdin(ctx, sandbox, command, input, stdout, stderr)
}

func TestReviewContainerEvidenceBridgePreservesPreparationBudget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                  string
		createdAgo            time.Duration
		callerRemaining       time.Duration
		explicitExpired       bool
		cancelled             bool
		expectedVerifyOnly    []bool
		expectedVerifications string
	}{
		{name: "live durable job", createdAgo: 2 * time.Minute, expectedVerifyOnly: []bool{false}, expectedVerifications: "verify\nverify\n"},
		{name: "earlier caller deadline", createdAgo: time.Minute, callerRemaining: 2 * time.Minute, expectedVerifyOnly: []bool{false}, expectedVerifications: "verify\nverify\n"},
		{name: "expired durable job", createdAgo: CodeReviewWorkspacePreparationTimeout + time.Second, expectedVerifyOnly: []bool{true}, expectedVerifications: "verify\n"},
		{name: "explicit expired preparation with live job", createdAgo: time.Second, explicitExpired: true, expectedVerifyOnly: []bool{true}, expectedVerifications: "verify\n"},
		{name: "cancelled parent stays cancelled", createdAgo: CodeReviewWorkspacePreparationTimeout + time.Second, cancelled: true, expectedVerifications: "verify\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			config := sourceReviewConfig(t,
				[]string{"mkdir -p .143/cache/go; printf 'package dep' > .143/cache/go/api.go; printf 'setup\\n' >> executions.log"},
				[]string{"printf 'verify\\n' >> permit; test -f .143/cache/go/api.go"},
				[]repoconfig.ReviewEvidenceEntry{{Path: ".143/cache/go", Description: "Pinned Go source", Category: models.CodeReviewEvidenceGoDependencies}})
			f := newReviewEvidenceGitFixtureWithConfig(t, config, false)
			o, sandbox := f.orchestrator(), f.sandbox()
			require.NoError(t, o.hydrateCodeReviewTrees(context.Background(), f.session, sandbox, f.remote, "", zerolog.Nop()), "the bridge fixture must begin with complete pinned source trees")
			require.NoError(t, EnsureCodeReviewWorkspaceEvidenceForTest(context.Background(), o, f.session, sandbox), "the bridge must prepare a reusable receipt before testing another turn")
			receiptPath := filepath.Join(f.workDir, ".git/143-review-evidence.json")
			receiptBefore, err := os.ReadFile(receiptPath)
			require.NoError(t, err, "the prepared receipt bytes must be captured before reuse")
			receiptInfo, err := os.Stat(receiptPath)
			require.NoError(t, err, "the prepared receipt file identity must be captured before reuse")
			provider := &reviewContainerBridgeProvider{managedReviewInputsProvider: managedReviewInputsProvider{
				localReviewEvidenceProvider: localReviewEvidenceProvider{diagnoseFailures: true, logFailure: t.Logf},
			}}
			tokens := &reviewConfigGitHubTokens{token: "fake-bridge-reuse-token"}
			o.provider, o.github = provider, tokens

			started := time.Now()
			created := started.Add(-tt.createdAgo)
			ctx := jobctx.WithJobCreatedAt(context.Background(), created)
			expectedDeadline := created.Add(CodeReviewWorkspacePreparationTimeout)
			if tt.callerRemaining > 0 {
				var cancel context.CancelFunc
				expectedDeadline = started.Add(tt.callerRemaining)
				ctx, cancel = context.WithDeadline(ctx, expectedDeadline)
				defer cancel()
			}
			if tt.explicitExpired {
				ctx = WithCodeReviewWorkspacePreparationDeadline(ctx, started.Add(-time.Second))
			}
			if tt.cancelled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before := time.Now()
			err = EnsureCodeReviewWorkspaceEvidenceForTest(ctx, o, f.session, sandbox)
			after := time.Now()
			if tt.cancelled {
				require.ErrorIs(t, err, context.Canceled, "activating the bridge must never revive a cancelled parent")
			} else {
				require.NoError(t, err, "complete prepared evidence must be reusable within the turn's permitted validation mode")
				require.NoError(t, ctx.Err(), "an aged job or preparation marker must leave the caller context live")
			}
			provider.mu.Lock()
			payloads := append([]reviewWorkspacePreparationPayload(nil), provider.preparations...)
			deadlines := append([]time.Time(nil), provider.deadlines...)
			authentications := append([]reviewGitAuthentication(nil), provider.authentications...)
			provider.mu.Unlock()
			var actualVerifyOnly []bool
			for _, payload := range payloads {
				actualVerifyOnly = append(actualVerifyOnly, payload.VerifyOnly)
			}
			require.Equal(t, tt.expectedVerifyOnly, actualVerifyOnly, "durable expiry must select exactly one local-only command and cancellation must execute none")
			for index, payload := range payloads {
				require.False(t, payload.Standalone, "the bridge must invoke the actual evidence engine rather than a standalone command")
				require.False(t, payload.AuthenticateGit, "complete warm and expired evidence must never enable authenticated hydration")
				require.False(t, deadlines[index].IsZero(), "every evidence command must have a bounded SDK deadline")
				require.Equal(t, float64(deadlines[index].UnixNano())/float64(time.Second), payload.DeadlineUnix, "serialized and SDK deadlines must describe the same absolute boundary")
				if payload.VerifyOnly {
					require.False(t, deadlines[index].Before(before.Add(CodeReviewWorkspaceVerificationTimeout)), "the expired local-only allowance must be the existing sixty-second window")
					require.False(t, deadlines[index].After(after.Add(CodeReviewWorkspaceVerificationTimeout)), "the expired bridge must never grant a fresh eight-minute setup budget")
				} else {
					require.Equal(t, expectedDeadline, deadlines[index], "a live turn must retain its job or earlier caller absolute deadline without renewal")
				}
			}
			expectedAuthentications := make([]reviewGitAuthentication, len(tt.expectedVerifyOnly))
			if tt.expectedVerifyOnly == nil {
				expectedAuthentications = nil
			}
			require.Equal(t, expectedAuthentications, authentications, "no bridge reuse command may carry Git authentication")
			require.Equal(t, 0, tokens.calls, "expired and warm reuse must never request another installation token")
			for _, expected := range []struct{ path, content string }{
				{filepath.Join(f.workDir, "executions.log"), "setup\n"},
				{filepath.Join(f.workDir, "permit"), tt.expectedVerifications},
			} {
				content, readErr := os.ReadFile(expected.path)
				require.NoError(t, readErr, "the bridge's source setup and verifier histories must remain readable")
				require.Equal(t, expected.content, string(content), "expired or cancelled turns must not rerun repository setup or arbitrary verifiers")
			}
			receiptAfter, err := os.ReadFile(receiptPath)
			require.NoError(t, err, "reuse must preserve a readable evidence receipt")
			afterInfo, err := os.Stat(receiptPath)
			require.NoError(t, err, "reuse must preserve the original receipt file identity")
			require.Equal(t, receiptBefore, receiptAfter, "bridge reuse must preserve the exact original receipt bytes")
			require.True(t, os.SameFile(receiptInfo, afterInfo), "bridge reuse must never replace the original receipt")
		})
	}
}
