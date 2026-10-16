package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestSharedReviewInputsHydratePromisedBlobsFromBitmapRemote(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", false)
	reviewInputsWritableCleanup(t, f.workDir)
	remote := strings.TrimPrefix(f.remote, "file://")
	out, err := exec.Command("/usr/bin/git", "-C", remote, "repack", "-adb").CombinedOutput()
	require.NoError(t, err, "the synthetic bare server must generate its bitmap: "+string(out))
	bitmaps, err := filepath.Glob(filepath.Join(remote, "objects/pack/*.bitmap"))
	require.NoError(t, err, "the bare server bitmap should be discoverable")
	require.NotEmpty(t, bitmaps, "the regression must negotiate against a bitmap-backed remote")
	configPath := filepath.Join(f.workDir, ".git/config")
	beforeConfig, err := os.ReadFile(configPath)
	require.NoError(t, err, "partial clone configuration must be captured before hydration")
	provider := &managedReviewInputsProvider{localReviewEvidenceProvider: localReviewEvidenceProvider{diagnoseFailures: true, logFailure: t.Logf}}
	tokens := &reviewInputsTokenSpy{}
	orchestrator := &Orchestrator{provider: provider, repositories: f.orchestrator().repositories, github: tokens, logger: zerolog.Nop()}
	session := reviewInputsSession(t, f.base, f.head)
	sandbox := f.sandbox()
	require.Error(t, orchestrator.execReviewEvidence(context.Background(), sandbox, reviewTreeEvidenceScript(f.workDir, f.base, f.head, ""), ""), "actual partial checkout must lack base-only promised source before hydration")
	require.NoError(t, orchestrator.ensureCodeReviewInputs(context.Background(), session, sandbox), "shared input coordinator must hydrate pinned blobs from a real bitmap-backed Git server")
	require.Equal(t, int64(1), tokens.calls.Load(), "only one explicit hydration authentication should be needed")
	afterConfig, err := os.ReadFile(configPath)
	require.NoError(t, err, "partial clone configuration must be readable after hydration")
	require.Equal(t, beforeConfig, afterConfig, "hydration negotiation and authentication must not persist Git configuration")
	manifestPath, patchPath := CodeReviewInputPaths(session.OrgID, session.ID, *session.RepositoryID, "example/repo", f.base, f.head)
	manifestBefore, err := os.ReadFile(filepath.Join(f.workDir, manifestPath))
	require.NoError(t, err, "captured immutable manifest must exist")
	patchBefore, err := os.ReadFile(filepath.Join(f.workDir, patchPath))
	require.NoError(t, err, "captured immutable patch must exist")
	require.NoError(t, os.Rename(remote, remote+"-offline"), "the real Git server must be physically unavailable for warm and source verification")
	require.NoError(t, orchestrator.ensureCodeReviewInputs(context.Background(), session, sandbox), "warm shared inputs must validate without the remote")
	require.Equal(t, int64(1), tokens.calls.Load(), "warm verification must not authenticate again")
	manifestAfter, err := os.ReadFile(filepath.Join(f.workDir, manifestPath))
	require.NoError(t, err, "offline manifest must remain readable")
	patchAfter, err := os.ReadFile(filepath.Join(f.workDir, patchPath))
	require.NoError(t, err, "offline patch must remain readable")
	require.Equal(t, manifestBefore, manifestAfter, "offline warm verification must preserve captured identity")
	require.Equal(t, patchBefore, patchAfter, "offline warm verification must preserve captured diff bytes")
	require.NoError(t, orchestrator.execReviewEvidence(context.Background(), sandbox, reviewTreeEvidenceScript(f.workDir, f.base, f.head, ""), ""), "complete captured base, head, and merge-base closures must verify offline")
	tests := []struct {
		name, revision, file, expected string
	}{
		{name: "removed base source", revision: f.base, file: "removed.go", expected: "base implementation removed by the PR\n"},
		{name: "base target source", revision: f.base, file: "target-only.go", expected: "target branch surrounding implementation\n"},
		{name: "head source", revision: f.head, file: "changed.go", expected: "PR head implementation\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command("/usr/bin/git", "-C", f.workDir, "-c", "remote.origin.promisor=false", "show", tt.revision+":"+tt.file)
			cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "pinned source must be available offline: "+string(output))
			require.Equal(t, tt.expected, string(output), "captured source bytes must match the pinned commit")
		})
	}
	cmd := exec.Command("/usr/bin/git", "-C", f.workDir, "-c", "remote.origin.promisor=false", "cat-file", "-e", f.historyBlob)
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
	require.Error(t, cmd.Run(), "scoped raw-object hydration must leave unrelated historical blobs absent")
}
