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

func TestReviewEvidenceHydratesPromisedBlobsFromBitmapRemote(t *testing.T) {
	t.Parallel()
	f := newReviewEvidenceGitFixture(t, "", true)
	remote := strings.TrimPrefix(f.remote, "file://")
	out, err := exec.Command("/usr/bin/git", "-C", remote, "repack", "-adb").CombinedOutput()
	require.NoError(t, err, "the isolated bare remote should prepare its real reachability bitmap: "+string(out))
	bitmaps, err := filepath.Glob(filepath.Join(remote, "objects/pack/*.bitmap"))
	require.NoError(t, err, "the fixture should locate its remote reachability bitmap")
	require.NotEmpty(t, bitmaps, "the raw-object regression requires a bitmap-backed upload-pack server")
	configPath := filepath.Join(f.workDir, ".git/config")
	beforeConfig, err := os.ReadFile(configPath)
	require.NoError(t, err, "the partial-clone configuration should be captured before hydration")
	o := f.orchestrator()
	require.Error(t, o.execReviewEvidence(context.Background(), f.sandbox(), reviewTreeEvidenceScript(f.workDir, f.base, f.head, ""), ""), "the actual filtered clone must begin with missing promised source blobs")
	require.NoError(t, o.hydrateCodeReviewTrees(context.Background(), f.session, f.sandbox(), f.remote, "", zerolog.Nop()), "raw object hydration must retrieve promised blobs even when bitmap negotiation treats their commits as common")
	afterConfig, err := os.ReadFile(configPath)
	require.NoError(t, err, "the clone configuration should remain readable after hydration")
	require.Equal(t, beforeConfig, afterConfig, "raw-object negotiation must remain command-scoped without persisting configuration or authentication")
	require.NoError(t, os.Rename(remote, remote+"-offline"), "the bitmap remote should become unavailable before source verification")
	require.NoError(t, o.execReviewEvidence(context.Background(), f.sandbox(), reviewTreeEvidenceScript(f.workDir, f.base, f.head, ""), ""), "all pinned source trees must be complete without contacting the promisor remote")
	tests := []struct {
		name, revision, file, expected string
	}{
		{name: "removed base source", revision: f.base, file: "removed.go", expected: "base implementation removed by the PR\n"},
		{name: "head source", revision: f.head, file: "changed.go", expected: "PR head implementation\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command("/usr/bin/git", "-C", f.workDir, "-c", "remote.origin.promisor=false", "show", tt.revision+":"+tt.file)
			cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "the captured source should be readable with the remote offline: "+string(out))
			require.Equal(t, tt.expected, string(out), "bitmap-backed hydration should preserve the exact captured source bytes")
		})
	}
	cmd := exec.Command("/usr/bin/git", "-C", f.workDir, "-c", "remote.origin.promisor=false", "cat-file", "-e", f.historyBlob)
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
	require.Error(t, cmd.Run(), "raw-object hydration must leave unrelated historical blobs absent")
}
