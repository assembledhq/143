package repoconfig

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInstallCommands_RendersGolangciLint(t *testing.T) {
	t.Parallel()

	commands, err := InstallCommands([]Dependency{
		{Name: "golangci-lint", Version: "2.5.0"},
	})
	require.NoError(t, err, "InstallCommands should accept a supported dependency")
	require.Len(t, commands, 1, "InstallCommands should return one command per dependency")
	require.Contains(t, commands[0], "golangci-lint/releases/download/v2.5.0/", "rendered command should pin the requested version")
	require.Contains(t, commands[0], "$HOME/.local/bin/golangci-lint", "rendered command should install to the sandbox user's bin")
}

func TestInstallCommands_RejectsUnknownDependency(t *testing.T) {
	t.Parallel()

	_, err := InstallCommands([]Dependency{{Name: "ruff", Version: "0.6.0"}})
	require.Error(t, err, "InstallCommands should refuse names that are not in the registry")
	require.Contains(t, err.Error(), `"ruff"`, "error should name the offending dependency")
}

func TestInstallCommands_RejectsNonSemverVersion(t *testing.T) {
	t.Parallel()

	_, err := InstallCommands([]Dependency{{Name: "golangci-lint", Version: "v2.5.0; rm -rf /"}})
	require.Error(t, err, "InstallCommands should reject versions that are not MAJOR.MINOR.PATCH so shell metacharacters cannot leak in")
	require.Contains(t, err.Error(), "MAJOR.MINOR.PATCH", "error should explain the expected format")
}
