package onefortythree_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetupTreatsOutdatedNodeAsInstallCandidate(t *testing.T) {
	t.Parallel()

	script, err := os.ReadFile("setup.sh")
	require.NoError(t, err, "test should read setup.sh")
	scriptText := string(script)

	require.Contains(t, scriptText, "needs_node_install()", "setup should centralize missing-or-outdated Node detection")
	require.Contains(t, scriptText, "needs_node_install && missing+=(node)", "setup should install or upgrade Node when the existing version is below the required major")
}
