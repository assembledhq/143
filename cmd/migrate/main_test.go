package main

import (
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
