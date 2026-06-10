package db

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGenerateOrgJoinTokenMatchesInstallPathGate pins the generator's output
// to the /install/{join_token} syntactic gate
// (^143j_[A-Za-z0-9]{12,64}$ in internal/api/handlers/cli_distribution.go).
// A generator drift toward base64 charsets ('-', '_') would mint tokens the
// installer route 404s.
func TestGenerateOrgJoinTokenMatchesInstallPathGate(t *testing.T) {
	t.Parallel()

	gate := regexp.MustCompile(`^143j_[A-Za-z0-9]{12,64}$`)
	seen := make(map[string]bool)
	for i := 0; i < 64; i++ {
		token, err := GenerateOrgJoinToken()
		require.NoError(t, err)
		require.Regexp(t, gate, token)
		require.False(t, seen[token], "tokens must be unique")
		seen[token] = true
	}
}

func TestUserCLITokenGeneration(t *testing.T) {
	t.Parallel()

	token, err := GenerateUserCLIToken()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(token, UserCLITokenPrefix))
	require.Equal(t, token[:13], UserCLITokenDisplayPrefix(token), `display prefix is "143u_" + 8 chars`)

	// Hashing matches the api_tokens scheme so the hash itself is the
	// deterministic lookup key.
	require.True(t, strings.HasPrefix(HashAPIToken(token), "sha256:"))
}
