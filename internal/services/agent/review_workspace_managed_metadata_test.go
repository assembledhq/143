package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestReviewPreparationRejectsUnsafeManagedMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, path, kind, phase string }{
		{name: "fresh lock FIFO", path: "143-review-preparation.lock", kind: "fifo", phase: "fresh"},
		{name: "fresh receipt FIFO", path: "143-review-evidence.json", kind: "fifo", phase: "fresh"},
		{name: "warm lock FIFO", path: "143-review-preparation.lock", kind: "fifo", phase: "warm"},
		{name: "warm receipt FIFO", path: "143-review-evidence.json", kind: "fifo", phase: "warm"},
		{name: "expired lock FIFO", path: "143-review-preparation.lock", kind: "fifo", phase: "expired"},
		{name: "expired receipt FIFO", path: "143-review-evidence.json", kind: "fifo", phase: "expired"},
		{name: "fresh lock symlink", path: "143-review-preparation.lock", kind: "symlink", phase: "fresh"},
		{name: "fresh receipt symlink", path: "143-review-evidence.json", kind: "symlink", phase: "fresh"},
		{name: "warm lock symlink", path: "143-review-preparation.lock", kind: "symlink", phase: "warm"},
		{name: "warm receipt symlink", path: "143-review-evidence.json", kind: "symlink", phase: "warm"},
		{name: "expired lock symlink", path: "143-review-preparation.lock", kind: "symlink", phase: "expired"},
		{name: "expired receipt symlink", path: "143-review-evidence.json", kind: "symlink", phase: "expired"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "printf '.\\n' >> executions.log", false)
			o := f.orchestrator()
			expectedRuns := ""
			if tt.phase != "fresh" {
				require.NoError(t, o.ensureCodeReviewWorkspaceEvidence(context.Background(), f.session, f.sandbox(), zerolog.Nop()), "initial preparation should create a healthy receipt before the managed metadata is poisoned")
				expectedRuns = ".\n"
			}
			target := filepath.Join(f.workDir, ".git", tt.path)
			if _, err := os.Lstat(target); err == nil {
				require.NoError(t, os.Remove(target), "fixture should replace only its managed metadata file")
			} else {
				require.True(t, os.IsNotExist(err), "fixture metadata should be absent or an existing regular file")
			}
			outside := filepath.Join(t.TempDir(), "outside")
			require.NoError(t, os.WriteFile(outside, []byte("untouched"), 0o600), "outside symlink target should expose any unsafe read/write cleanup")
			if tt.kind == "fifo" {
				require.NoError(t, syscall.Mkfifo(target, 0o600), "managed metadata fixture should create an unopened FIFO")
			} else {
				require.NoError(t, os.Symlink(outside, target), "managed metadata fixture should link to an outside regular file")
			}
			tokens := &reviewConfigGitHubTokens{token: "fake-managed-metadata-token"}
			o.github = tokens
			deadline := time.Now().Add(250 * time.Millisecond)
			if tt.phase == "expired" {
				deadline = time.Now().Add(-time.Second)
			}
			ctx := WithCodeReviewWorkspacePreparationDeadline(context.Background(), deadline)
			started := time.Now()
			require.Error(t, o.ensureCodeReviewWorkspaceEvidence(ctx, f.session, f.sandbox(), zerolog.Nop()), "special or linked managed metadata must reject readiness instead of blocking beyond preparation")
			require.Less(t, time.Since(started), time.Second, "managed metadata rejection must finish independently of FIFO writers and cancellation cleanup")
			require.Equal(t, 0, tokens.calls, "managed metadata must fail before any installation-token lookup")
			outsideBytes, err := os.ReadFile(outside)
			require.NoError(t, err, "outside metadata target should remain readable")
			require.Equal(t, "untouched", string(outsideBytes), "lock, receipt and cancellation cleanup must never alter linked outside files")
			runs, err := os.ReadFile(filepath.Join(f.workDir, "executions.log"))
			if expectedRuns == "" {
				require.True(t, os.IsNotExist(err), "fresh rejected metadata must execute no repository recipe")
			} else {
				require.NoError(t, err, "previous recipe history should survive rejection")
				require.Equal(t, expectedRuns, string(runs), "warm and expired rejection must never rerun preparation")
			}
		})
	}
}

func TestReviewPreparationManagedMetadataSafetyWithoutRepositoryURL(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, path, kind string }{
		{name: "lock FIFO", path: "143-review-preparation.lock", kind: "fifo"},
		{name: "receipt FIFO", path: "143-review-evidence.json", kind: "fifo"},
		{name: "lock symlink", path: "143-review-preparation.lock", kind: "symlink"},
		{name: "receipt symlink", path: "143-review-evidence.json", kind: "symlink"},
		{name: "oversized regular receipt", path: "143-review-evidence.json", kind: "oversized"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o700), "local-only controller fixture should contain literal metadata")
			target := filepath.Join(root, ".git", tt.path)
			outside := filepath.Join(t.TempDir(), "outside")
			require.NoError(t, os.WriteFile(outside, []byte("untouched"), 0o600), "outside file should expose any unintended metadata mutation")
			switch tt.kind {
			case "fifo":
				require.NoError(t, syscall.Mkfifo(target, 0o600), "local-only payload should face an unopened managed FIFO")
			case "symlink":
				require.NoError(t, os.Symlink(outside, target), "local-only payload should face linked managed metadata")
			case "oversized":
				file, err := os.Create(target)
				require.NoError(t, err, "receipt bound fixture should create a sparse regular file")
				require.NoError(t, file.Truncate(256*1024*1024+1), "sparse receipt should exceed the finite input bound without allocating its content")
				require.NoError(t, file.Close(), "oversized receipt fixture should close before preparation")
			}
			payload := reviewWorkspacePreparationPayload{WorkDir: root, Receipt: json.RawMessage(`{"version":1}`), VerifyGit: "true", VerifyTrees: "true", VerifyTools: "true", Prepare: "printf launched > permit", TimeoutSeconds: 0.25, CancelPath: filepath.Join(root, "cancel")}
			o := &Orchestrator{provider: uncancelledLocalReviewEvidenceProvider{}}
			started := time.Now()
			err := o.execReviewPreparationPayload(context.Background(), &Sandbox{WorkDir: root}, payload, reviewGitAuthentication{}, zerolog.Nop())
			require.Error(t, err, "managed metadata must fail closed even when no remote/configuration guard is requested")
			require.Less(t, time.Since(started), time.Second, "nonblocking metadata handling must finish under the wrapper's own short absolute budget")
			require.NoFileExists(t, filepath.Join(root, "permit"), "metadata rejection must start no recipe child")
			require.NoFileExists(t, payload.CancelPath+".done", "terminated wrapper must confirm completion so Go cleanup can remove its done marker")
			outsideBytes, err := os.ReadFile(outside)
			require.NoError(t, err, "outside target should survive the rejected invocation")
			require.Equal(t, "untouched", string(outsideBytes), "local-only handling must not follow managed symlinks")
		})
	}
}
