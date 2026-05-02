package db

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCopyCodingCredentialsMigrationFiltersUserCredentialProviders(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../migrations/000110_copy_coding_credentials.up.sql")
	require.NoError(t, err, "test should read the coding credential copy migration")

	sql := string(body)
	allowedProviders := "('openai', 'anthropic', 'gemini', 'amp', 'pi', 'openrouter')"
	require.Contains(t, sql,
		"WHERE is_team_default = false\n  AND provider IN "+allowedProviders,
		"personal user credential copy should include only coding-agent providers")
	require.Contains(t, sql,
		"WHERE uc.is_team_default = true\n  AND uc.provider IN "+allowedProviders,
		"team-default user credential copy should include only coding-agent providers")
}
