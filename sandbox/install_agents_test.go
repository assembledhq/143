package sandbox_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSandboxDockerfileVerifiesOpenCodeInstall(t *testing.T) {
	t.Parallel()

	dockerfile, err := os.ReadFile("Dockerfile")
	require.NoError(t, err, "test should read sandbox Dockerfile")
	require.Contains(t, string(dockerfile), "opencode --version", "sandbox Dockerfile should verify the OpenCode CLI after npm install")
}
