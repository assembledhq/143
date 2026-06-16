package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveMigrationSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		paths    map[string]bool
		expected string
	}{
		{
			name: "prefers repo relative migrations directory",
			paths: map[string]bool{
				"migrations":  true,
				"/migrations": true,
			},
			expected: "file://migrations",
		},
		{
			name: "falls back to container absolute migrations directory",
			paths: map[string]bool{
				"/migrations": true,
			},
			expected: "file:///migrations",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual, err := resolveMigrationSource(func(path string) bool {
				return tt.paths[path]
			})

			require.NoError(t, err, "resolveMigrationSource should find an available migrations directory")
			require.Equal(t, tt.expected, actual, "resolveMigrationSource should return the expected source URL")
		})
	}
}

func TestResolveMigrationSourceReturnsErrorWhenNoDirectoryExists(t *testing.T) {
	t.Parallel()

	_, err := resolveMigrationSource(func(path string) bool {
		return false
	})

	require.Error(t, err, "resolveMigrationSource should fail when no migrations directory is available")
}

func TestMigrationVersionsAreUnique(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.sql"))
	require.NoError(t, err, "should glob migration files without error")

	versionPattern := regexp.MustCompile(`^(\d{6})_.+\.(up|down)\.sql$`)
	seen := make(map[string]string, len(files))

	for _, path := range files {
		base := filepath.Base(path)
		matches := versionPattern.FindStringSubmatch(base)
		require.Len(t, matches, 3, "migration filename should include a 6-digit version and direction")

		key := matches[1] + "." + matches[2]

		if previous, ok := seen[key]; ok {
			require.Failf(
				t,
				"duplicate migration version-direction",
				"migration slot %s is used by both %s and %s",
				key,
				previous,
				base,
			)
		}
		seen[key] = base
	}
}

func TestSlackOrgSelectionsMigrationUsesPostPreviewSlot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		filename    string
		shouldExist bool
	}{
		{
			name:        "up migration uses version after preview current groups",
			filename:    "000197_slack_org_selections.up.sql",
			shouldExist: true,
		},
		{
			name:        "down migration uses version after preview current groups",
			filename:    "000197_slack_org_selections.down.sql",
			shouldExist: true,
		},
		{
			name:        "up migration does not occupy preview current groups slot",
			filename:    "000196_slack_org_selections.up.sql",
			shouldExist: false,
		},
		{
			name:        "down migration does not occupy preview current groups slot",
			filename:    "000196_slack_org_selections.down.sql",
			shouldExist: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := os.Stat(filepath.Join("..", "..", "migrations", tt.filename))
			if tt.shouldExist {
				require.NoError(t, err, "slack org selections migration should use version 000197")
				return
			}
			require.True(t, os.IsNotExist(err), "slack org selections migration should not use version 000196")
		})
	}
}

func TestMigrationsDoNotUseConcurrentIndexes(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.sql"))
	require.NoError(t, err, "should glob migration files without error")

	for _, path := range files {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()

			contents, err := os.ReadFile(path)
			require.NoError(t, err, "migration file should be readable")
			sql := stripSQLLineComments(string(contents))
			require.NotContains(
				t,
				strings.ToUpper(sql),
				"CREATE INDEX CONCURRENTLY",
				"migration files run inside a transaction and must not create indexes concurrently",
			)
			require.NotContains(
				t,
				strings.ToUpper(sql),
				"DROP INDEX CONCURRENTLY",
				"migration files run inside a transaction and must not drop indexes concurrently",
			)
		})
	}
}

func stripSQLLineComments(contents string) string {
	lines := strings.Split(contents, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "--"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n")
}
