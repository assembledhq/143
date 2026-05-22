package sandboxdeps

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGolangciLintInstallUsesPinnedReleaseArchive(t *testing.T) {
	t.Parallel()

	var gotCmd string
	dep := golangciLint{}
	err := dep.Install(context.Background(), func(_ context.Context, cmd string, _, _ io.Writer) (int, error) {
		gotCmd = cmd
		return 0, nil
	}, "2.10.1")

	require.NoError(t, err, "Install should accept a concrete golangci-lint version")
	require.Contains(t, gotCmd, "https://github.com/golangci/golangci-lint/releases/download/v2.10.1/golangci-lint-2.10.1-${os}-${arch}.tar.gz", "Install should download the pinned release archive")
	require.Contains(t, gotCmd, `curl -fsSL --connect-timeout 10 --max-time 120 -o "$archive" "$url"`, "Install should use bounded curl download to a file")
	require.NotContains(t, gotCmd, "raw.githubusercontent.com/golangci/golangci-lint/master/install.sh", "Install should not depend on a mutable installer script from master")
	require.NotContains(t, gotCmd, "| sh", "Install should not mask curl failures through a shell pipeline")
}

func TestGolangciLintInstallRejectsInvalidVersion(t *testing.T) {
	t.Parallel()

	called := false
	dep := golangciLint{}
	err := dep.Install(context.Background(), func(_ context.Context, _ string, _, _ io.Writer) (int, error) {
		called = true
		return 0, nil
	}, "2.10.1; rm -rf /")

	require.Error(t, err, "Install should reject versions that are unsafe to interpolate into shell")
	require.Contains(t, err.Error(), "invalid version pin", "Install should explain that the version pin is invalid")
	require.False(t, called, "Install should reject invalid versions before executing any sandbox command")
}

func TestGolangciLintInstallReturnsExecError(t *testing.T) {
	t.Parallel()

	dep := golangciLint{}
	err := dep.Install(context.Background(), func(_ context.Context, _ string, _, _ io.Writer) (int, error) {
		return -1, errors.New("sandbox exec failed")
	}, "2.10.1")

	require.Error(t, err, "Install should return sandbox exec errors")
	require.Contains(t, err.Error(), "sandbox exec failed", "Install should include the underlying exec error")
}

func TestGolangciLintInstallReturnsStderrOnNonZeroExit(t *testing.T) {
	t.Parallel()

	dep := golangciLint{}
	err := dep.Install(context.Background(), func(_ context.Context, _ string, _, stderr io.Writer) (int, error) {
		_, writeErr := stderr.Write([]byte("download failed"))
		require.NoError(t, writeErr, "test stderr write should succeed")
		return 1, nil
	}, "2.10.1")

	require.Error(t, err, "Install should return an error for non-zero installer exits")
	require.Contains(t, err.Error(), "download failed", "Install should include stderr output for failed installs")
}
