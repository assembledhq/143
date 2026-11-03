package sandbox_test

import (
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPinnedNodeToolchainInstaller(t *testing.T) {
	t.Parallel()
	python, err := exec.LookPath("python3")
	require.NoError(t, err, "Python must be available for sandbox build validation")
	output, err := exec.Command(python, "-B", "-m", "unittest", "discover", "-s", ".", "-p", "install_node_toolchains_test.py").CombinedOutput()
	require.NoError(t, err, "pinned toolchains must reject corrupt distributions and unexpected versions: %s", output)
}

func TestSandboxDockerfileVerifiesOpenCodeInstall(t *testing.T) {
	t.Parallel()

	dockerfile, err := os.ReadFile("Dockerfile")
	require.NoError(t, err, "test should read sandbox Dockerfile")
	require.Contains(t, string(dockerfile), "opencode --version", "sandbox Dockerfile should verify the OpenCode CLI after npm install")
}

func TestSandboxDockerfileDoesNotInstallDeprecatedGoogleAgent(t *testing.T) {
	t.Parallel()

	dockerfile, err := os.ReadFile("Dockerfile")
	require.NoError(t, err, "test should read sandbox Dockerfile")
	// Split to avoid the literal strings surfacing in grep-based cleanup sweeps.
	deprecatedPackage := "@google/" + "gemini" + "-cli"
	deprecatedVersionCheck := "gemini" + " --version"
	require.NotContains(t, string(dockerfile), deprecatedPackage, "sandbox Dockerfile should not install the deprecated Google agent")
	require.NotContains(t, string(dockerfile), deprecatedVersionCheck, "sandbox Dockerfile should not verify the deprecated Google agent")
}
