package agent

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWorkspaceReadinessWithRealGitCheckout(t *testing.T) {
	t.Parallel()
	const branch = "143/test/review"
	tests := []struct {
		name         string
		checkout     string
		removeBranch bool
		moveBranch   bool
		wantReady    bool
	}{
		{name: "prepared named checkout", wantReady: true},
		{name: "reviewer detached at prepared head", checkout: "--detach", wantReady: true},
		{name: "detached before branch preparation", checkout: "--detach", removeBranch: true},
		{name: "detached branch points at another commit", checkout: "--detach", moveBranch: true},
		{name: "another named branch at the same head", checkout: "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := filepath.Join(t.TempDir(), "repo's checkout")
			git := func(args ...string) string {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, "Git fixture command should succeed: %s", out)
				return strings.TrimSpace(string(out))
			}
			out, err := exec.Command("git", "init", "-q", repo).CombinedOutput()
			require.NoError(t, err, "initialize isolated Git repository: %s", out)
			git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "base")
			base := git("rev-parse", "HEAD")
			git("checkout", "-q", "-b", branch)
			git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "review head")
			head := git("rev-parse", "HEAD")
			switch tt.checkout {
			case "--detach":
				git("checkout", "-q", "--detach", head)
			case "other":
				git("checkout", "-q", "-b", "other")
			}
			if tt.removeBranch {
				git("branch", "-D", branch)
			}
			if tt.moveBranch {
				git("branch", "-f", branch, base)
			}
			provider := &testInternalSandboxProvider{execFn: func(command string, stdout, stderr io.Writer) (int, error) {
				cmd := exec.Command("sh", "-c", command)
				cmd.Stdout, cmd.Stderr = stdout, stderr
				if err := cmd.Run(); err != nil {
					if exit, ok := err.(*exec.ExitError); ok {
						return exit.ExitCode(), nil
					}
					return -1, err
				}
				return 0, nil
			}}
			err = waitForSandboxWorkspaceReady(context.Background(), provider, &Sandbox{WorkDir: repo}, branch, head, 150*time.Millisecond, time.Millisecond, 4*time.Millisecond, nil)
			if tt.wantReady {
				require.NoError(t, err, "authoritative checkout should be ready even after an allowed detached review checkout")
			} else {
				require.ErrorIs(t, err, ErrSandboxWorkspaceNotReady, "missing or mismatched working-branch ownership must not pass readiness")
			}
		})
	}
}
