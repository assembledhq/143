package agent

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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

func TestWorkspaceReadinessWaitsForPreparedInputs(t *testing.T) {
	t.Parallel()
	var probes, checks int
	provider := &testInternalSandboxProvider{execFn: func(_ string, stdout, _ io.Writer) (int, error) {
		probes++
		_, err := io.WriteString(stdout, "head\nreview\nhead\n")
		return 0, err
	}}
	err := waitForSandboxWorkspaceReady(context.Background(), provider, &Sandbox{WorkDir: "/workspace"}, "review", "head", time.Second, time.Millisecond, 4*time.Millisecond, nil, func(context.Context) (bool, error) {
		checks++
		return checks >= 3, nil
	})
	require.NoError(t, err, "workspace readiness should wait until both checkout and prepared inputs are complete")
	require.Equal(t, 3, probes, "matching HEAD must not skip the input preparation barrier")
}

func TestWorkspaceReadinessRejectsUnsafeMetadataWithoutBlocking(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, path string
		fifo, link bool
		head       string
	}{
		{name: "FIFO HEAD", path: "HEAD", fifo: true},
		{name: "linked HEAD", path: "HEAD", link: true},
		{name: "path traversal HEAD", path: "HEAD", head: "ref: refs/heads/../../../outside"},
		{name: "Git pointer layout", path: ".git", head: "gitdir: /outside"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := t.TempDir()
			gitdir := filepath.Join(repo, ".git")
			require.NoError(t, os.MkdirAll(gitdir, 0o700), "isolated readiness metadata directory should exist")
			target := filepath.Join(gitdir, tt.path)
			if tt.path == ".git" {
				require.NoError(t, os.Remove(gitdir), "pointer fixture should replace empty metadata directory")
				target = gitdir
			}
			switch {
			case tt.fifo:
				require.NoError(t, syscall.Mkfifo(target, 0o600), "readiness FIFO fixture should remain unopened")
			case tt.link:
				require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "outside"), target), "readiness must reject linked metadata")
			default:
				require.NoError(t, os.WriteFile(target, []byte(tt.head), 0o600), "readiness fixture should store unsafe pointer metadata")
			}
			command, err := reviewWorkspaceReadyProbeCommand(repo, "143/review")
			require.NoError(t, err, "trusted bounded metadata probe should render")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, "/bin/sh", "-c", command).CombinedOutput()
			require.Error(t, err, "unsafe metadata must fail without invoking Git/config/includes: %s", out)
			require.NoError(t, ctx.Err(), "regular-file and path checks must reject FIFO/links/traversal before deadline")
		})
	}
}
