package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDatabaseURLFromEnv(t *testing.T) {
	t.Parallel()

	require.Equal(t, "postgres://example", databaseURLFromLookup(func(key string) string {
		require.Equal(t, "DATABASE_URL", key, "databaseURLFromLookup should read the database URL env var")
		return "postgres://example"
	}), "databaseURLFromLookup should prefer DATABASE_URL")
}

func TestTimeoutFromEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     string
		expected  time.Duration
		expectErr bool
	}{
		{name: "default", expected: defaultTimeout},
		{name: "custom duration", value: "2s", expected: 2 * time.Second},
		{name: "invalid duration", value: "soon", expectErr: true},
		{name: "zero duration", value: "0s", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual, err := timeoutFromLookup(func(key string) string {
				require.Equal(t, "WAIT_POSTGRES_TIMEOUT", key, "timeoutFromLookup should read the timeout env var")
				return tt.value
			})
			if tt.expectErr {
				require.Error(t, err, "timeoutFromEnv should reject invalid timeout")
				return
			}
			require.NoError(t, err, "timeoutFromEnv should parse timeout")
			require.Equal(t, tt.expected, actual, "timeoutFromEnv should return expected timeout")
		})
	}
}
