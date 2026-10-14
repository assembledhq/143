package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReviewGitVerificationPreservesTrackedModesAndSymlinks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		maskPath  string
		maskFlag  string
		modify    func(t *testing.T, workDir string)
		wantError bool
	}{
		{name: "unchanged executable and symlink remain valid"},
		{
			name: "tracked text attributes retain normal newline conversion",
			modify: func(t *testing.T, workDir string) {
				require.NoError(t, os.WriteFile(filepath.Join(workDir, "normalized.txt"), []byte("tracked text\r\n"), 0o600), "fixture should use equivalent CRLF worktree text")
			},
		},
		{
			name: "removed executable bit is detected despite filemode false",
			modify: func(t *testing.T, workDir string) {
				require.NoError(t, os.Chmod(filepath.Join(workDir, "mode.sh"), 0o600), "fixture should remove the tracked executable permission")
			},
			wantError: true,
		},
		{
			name:     "added executable bit is detected despite a skipped index entry",
			maskPath: "changed.go", maskFlag: "--skip-worktree",
			modify: func(t *testing.T, workDir string) {
				require.NoError(t, os.Chmod(filepath.Join(workDir, "changed.go"), 0o700), "fixture should make a tracked nonexecutable file executable")
			},
			wantError: true,
		},
		{
			name:     "symlink target edit is detected despite assume unchanged",
			maskPath: "tracked-link", maskFlag: "--assume-unchanged",
			modify: func(t *testing.T, workDir string) {
				link := filepath.Join(workDir, "tracked-link")
				require.NoError(t, os.Remove(link), "fixture should remove the original tracked symlink")
				require.NoError(t, os.Symlink("renamed.go", link), "fixture should point the tracked symlink at different source")
			},
			wantError: true,
		},
		{
			name:     "symlink replaced by regular file is detected despite skipped entry",
			maskPath: "tracked-link", maskFlag: "--skip-worktree",
			modify: func(t *testing.T, workDir string) {
				link := filepath.Join(workDir, "tracked-link")
				require.NoError(t, os.Remove(link), "fixture should remove its tracked symlink before replacing its type")
				require.NoError(t, os.WriteFile(link, []byte("changed.go"), 0o600), "fixture should replace the symlink with a regular file containing its original target")
			},
			wantError: true,
		},
		{
			name:     "regular file replaced by symlink is detected despite assume unchanged",
			maskPath: "mode.sh", maskFlag: "--assume-unchanged",
			modify: func(t *testing.T, workDir string) {
				file := filepath.Join(workDir, "mode.sh")
				require.NoError(t, os.Remove(file), "fixture should remove its tracked regular file before replacing its type")
				require.NoError(t, os.Symlink("changed.go", file), "fixture should replace a tracked regular file with a symlink")
			},
			wantError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "", false)
			git := func(args ...string) string {
				t.Helper()
				command := exec.Command("git", append([]string{"-C", f.workDir}, args...)...)
				command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
				output, err := command.CombinedOutput()
				require.NoError(t, err, "tracked-mode Git fixture command should succeed (args=%v): %s", args, output)
				return strings.TrimSpace(string(output))
			}
			require.NoError(t, os.WriteFile(filepath.Join(f.workDir, "mode.sh"), []byte("#!/bin/sh\nprintf source-only\n"), 0o700), "fixture should contain an executable tracked script without running it")
			require.NoError(t, os.Symlink("changed.go", filepath.Join(f.workDir, "tracked-link")), "fixture should contain a tracked symlink to source")
			require.NoError(t, os.WriteFile(filepath.Join(f.workDir, "normalized.txt"), []byte("tracked text\n"), 0o600), "fixture should contain normalized tracked text")
			require.NoError(t, os.WriteFile(filepath.Join(f.workDir, ".gitattributes"), []byte("normalized.txt text eol=lf\n"), 0o600), "fixture should declare normal tracked newline conversion")
			git("add", "mode.sh", "tracked-link", "normalized.txt", ".gitattributes")
			git("-c", "user.name=Review fixture", "-c", "user.email=review@example.com", "commit", "-qm", "tracked mode and symlink evidence")
			pinned := git("rev-parse", "HEAD")
			git("config", "core.filemode", "false")
			if tt.maskFlag != "" {
				git("update-index", tt.maskFlag, "--", tt.maskPath)
			}
			if tt.modify != nil {
				tt.modify(t, f.workDir)
			}
			git("diff", "--quiet", "HEAD", "--")

			index := filepath.Join(f.workDir, ".git", "index")
			fixed := time.Unix(1723456789, 123456789)
			require.NoError(t, os.Chtimes(index, fixed, fixed), "fixture should give the original index an observable stable modification time")
			before, err := os.ReadFile(index)
			require.NoError(t, err, "original index bytes should be captured before independent verification")
			beforeInfo, err := os.Stat(index)
			require.NoError(t, err, "original index modification time should be captured before verification")

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err = f.orchestrator().execReviewEvidence(ctx, f.sandbox(), reviewPinnedCheckoutVerificationScript(f.workDir, pinned, pinned), "")
			if tt.wantError {
				require.Error(t, err, "owned-index verification should reject tracked mode or symlink edits hidden from ordinary Git diff")
			} else {
				require.NoError(t, err, "owned-index verification should accept unchanged tracked modes and normal attribute conversions")
			}
			after, err := os.ReadFile(index)
			require.NoError(t, err, "original index should remain readable after successful or failed verification")
			afterInfo, err := os.Stat(index)
			require.NoError(t, err, "original index modification time should remain available after verification")
			require.Equal(t, before, after, "verification must preserve every byte of the caller-owned index including its masking flags")
			require.Equal(t, beforeInfo.ModTime(), afterInfo.ModTime(), "verification must not refresh or rewrite the caller-owned index modification time")
		})
	}
}
