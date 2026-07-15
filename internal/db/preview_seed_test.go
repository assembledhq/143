package db

import (
	"path/filepath"
	"regexp"
	"testing"

	"github.com/assembledhq/143/internal/demoseed"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestPreviewSeedUsers(t *testing.T) {
	t.Parallel()

	body, err := demoseed.ReadAndScanSeed(filepath.Join("..", "..", demoseed.DefaultSeedPath))
	require.NoError(t, err, "test should read preview seed SQL")
	sql := string(body)

	tests := []struct {
		name        string
		id          string
		email       string
		displayName string
		role        string
	}{
		{name: "admin", id: "00000000-0000-4000-a000-000000000002", email: "preview-admin@143.dev", displayName: "Ada Lovelace", role: "admin"},
		{name: "member", id: "00000000-0000-4000-a000-000000000003", email: "preview-member@143.dev", displayName: "Grace Hopper", role: "member"},
		{name: "builder", id: "00000000-0000-4000-a000-000000000004", email: "preview-builder@143.dev", displayName: "Alan Turing", role: "builder"},
		{name: "viewer", id: "00000000-0000-4000-a000-000000000005", email: "preview-viewer@143.dev", displayName: "Dennis Ritchie", role: "viewer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Contains(t, sql, "'"+tt.email+"'", "preview seed should include the expected login email")
			userPattern := regexp.MustCompile(`(?s)'` + regexp.QuoteMeta(tt.email) + `'.*'` + regexp.QuoteMeta(tt.displayName) + `'.*'` + regexp.QuoteMeta(tt.role) + `'`)
			require.True(t, userPattern.MatchString(sql), "preview seed should assign the expected legacy role")
			passwordHashPattern := regexp.MustCompile(`(?s)'` + regexp.QuoteMeta(tt.email) + `'.*'` + regexp.QuoteMeta(tt.displayName) + `'.*'` + regexp.QuoteMeta(tt.role) + `',\s+-- bcrypt hash of "preview" \(cost 10\)\s+'\$2y\$10\$MtyCwm3KVYgmLvAinVwMHO3c65omeHXqqyIqwlz9JXJ30\.5V2fyAe'`)
			require.True(t, passwordHashPattern.MatchString(sql), "preview seed should make users sign in with the documented preview password")
			require.Contains(t, sql, "'"+tt.id+"'::uuid,\n    '00000000-0000-4000-a000-000000000001'::uuid,\n    '"+tt.role+"'", "preview seed should assign the expected membership role")
		})
	}

	hashes := regexp.MustCompile(`\$2[ayb]\$10\$[A-Za-z0-9./]{53}`).FindAllString(sql, -1)
	require.NotEmpty(t, hashes, "preview seed should include the documented preview password hash")
	for _, hash := range hashes {
		require.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte("preview")), "preview seed password hash should authenticate the documented preview password")
	}
}
