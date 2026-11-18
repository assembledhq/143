package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// BenchmarkReviewCheckoutBatch measures a locally available captured head in a
// fresh no-checkout clone. The optional delay is synthetic per Exec overhead.
func BenchmarkReviewCheckoutBatch(b *testing.B) {
	source := b.TempDir()
	git := func(b *testing.B, directory string, args ...string) string {
		b.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = directory
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(b, err, "synthetic repository command should succeed: %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	git(b, source, "init", "-qb", "main")
	git(b, source, "config", "user.name", "Synthetic Benchmark")
	git(b, source, "config", "user.email", "benchmark@example.invalid")
	for i := range 256 {
		content := strings.Repeat(fmt.Sprintf("synthetic content for file %d\n", i), 256)
		path := filepath.Join(source, fmt.Sprintf("file-%04d.txt", i))
		require.NoError(b, os.WriteFile(path, []byte(content), 0o600), "synthetic source file should exist")
	}
	git(b, source, "add", ".")
	git(b, source, "commit", "-qm", "captured head")
	head := git(b, source, "rev-parse", "HEAD")

	modes := []struct {
		name    string
		calls   int
		batched bool
	}{
		{name: "separate", calls: 3},
		{name: "batched", calls: 2, batched: true},
	}
	for _, delay := range []time.Duration{0, 25 * time.Millisecond} {
		b.Run("synthetic-exec-delay="+delay.String(), func(b *testing.B) {
			for _, mode := range modes {
				b.Run(mode.name, func(b *testing.B) {
					var calls int
					local := localReviewEvidenceProvider{}
					provider := reviewCheckoutExecStub{exec: func(ctx context.Context, sandbox *Sandbox, command string, stdout, stderr io.Writer) (int, error) {
						calls++
						if delay > 0 {
							timer := time.NewTimer(delay)
							defer timer.Stop()
							select {
							case <-ctx.Done():
								return -1, ctx.Err()
							case <-timer.C:
							}
						}
						return local.Exec(ctx, sandbox, command, stdout, stderr)
					}}
					o := &Orchestrator{provider: provider}
					root := b.TempDir()
					directory := filepath.Join(root, "checkout")
					sandbox := &Sandbox{WorkDir: directory, Env: map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null"}}
					for b.Loop() {
						b.StopTimer()
						git(b, root, "clone", "-q", "--no-local", "--no-checkout", source, directory)
						b.StartTimer()
						if mode.batched {
							require.NoError(b, o.checkoutCodeReviewHead(b.Context(), sandbox, source, "", 42, "review", head), "batched checkout should succeed")
						} else {
							benchmarkSeparateLocalReviewCheckout(b, provider, sandbox, head)
						}
						b.StopTimer()
						require.Equal(b, head, git(b, directory, "rev-parse", "HEAD"), "both modes must end at the captured head")
						require.FileExists(b, filepath.Join(directory, "file-0000.txt"), "both modes must materialize the tracked worktree")
						require.NoError(b, os.RemoveAll(directory), "the next iteration should start from a fresh clone")
						b.StartTimer()
					}
					require.Equal(b, b.N*mode.calls, calls, "executor count should match the measured path")
					b.ReportMetric(float64(calls)/float64(b.N), "execs/op")
				})
			}
		})
	}
}

// This is the original checkout's local-success path. The benchmark fixture
// guarantees that the captured commit is already present.
func benchmarkSeparateLocalReviewCheckout(b *testing.B, provider SandboxProvider, sandbox *Sandbox, head string) {
	b.Helper()
	ctx, cancel := context.WithTimeout(b.Context(), 2*time.Minute)
	defer cancel()
	exit, err := provider.Exec(ctx, sandbox, "git cat-file -e '"+head+"^{commit}'", io.Discard, io.Discard)
	require.NoError(b, err, "local commit probe should execute")
	require.Zero(b, exit, "the captured commit should already be present")
	var stderr bytes.Buffer
	exit, err = provider.Exec(ctx, sandbox, "git checkout -B 'review' '"+head+"'", io.Discard, &stderr)
	require.NoError(b, err, "checkout should execute")
	require.Zero(b, exit, "checkout should succeed: %s", stderr.String())
	var stdout bytes.Buffer
	stderr.Reset()
	exit, err = provider.Exec(ctx, sandbox, "git rev-parse HEAD", &stdout, &stderr)
	require.NoError(b, err, "HEAD verification should execute")
	require.Zero(b, exit, "HEAD should resolve: %s", stderr.String())
	require.Equal(b, head, strings.TrimSpace(stdout.String()), "checkout should resolve the pinned head")
}
