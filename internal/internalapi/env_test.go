package internalapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodingSessionIDEnvVarIsPortable(t *testing.T) {
	t.Parallel()

	require.Equal(t, "CODING_SESSION_ID", CodingSessionIDEnvVar, "coding session environment variable should use the platform-neutral public contract")
	require.Regexp(t, `^[A-Za-z_][A-Za-z0-9_]*$`, CodingSessionIDEnvVar, "coding session environment variable should be portable across POSIX-compatible process launchers")
}
