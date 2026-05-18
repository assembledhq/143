package db

import (
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestPreviewSeedUsers(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../../.143/seed.sql")
	require.NoError(t, err, "test should read preview seed SQL")
	sql := string(body)

	tests := []struct {
		name        string
		id          string
		email       string
		displayName string
		role        string
	}{
		{name: "admin", id: "00000000-0000-4000-a000-000000000002", email: "preview-admin@143.dev", displayName: "Preview Admin", role: "admin"},
		{name: "member", id: "00000000-0000-4000-a000-000000000003", email: "preview-member@143.dev", displayName: "Preview Member", role: "member"},
		{name: "builder", id: "00000000-0000-4000-a000-000000000004", email: "preview-builder@143.dev", displayName: "Preview Builder", role: "builder"},
		{name: "viewer", id: "00000000-0000-4000-a000-000000000005", email: "preview-viewer@143.dev", displayName: "Preview Viewer", role: "viewer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Contains(t, sql, "'"+tt.email+"'", "preview seed should include the expected login email")
			userPattern := regexp.MustCompile(`(?s)'` + regexp.QuoteMeta(tt.email) + `'.*'` + regexp.QuoteMeta(tt.displayName) + `'.*'` + regexp.QuoteMeta(tt.role) + `'`)
			require.True(t, userPattern.MatchString(sql), "preview seed should assign the expected legacy role")
			require.Contains(t, sql, "'"+tt.id+"'::uuid,\n    '00000000-0000-4000-a000-000000000001'::uuid,\n    '"+tt.role+"'", "preview seed should assign the expected membership role")
		})
	}

	hashes := regexp.MustCompile(`\$2[ayb]\$10\$[A-Za-z0-9./]{53}`).FindAllString(sql, -1)
	require.NotEmpty(t, hashes, "preview seed should include bcrypt password hashes")
	for _, hash := range hashes {
		require.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte("preview")), "every preview seed password hash should match the shared preview password")
	}
}
