package db

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewPoolConfigAppliesMaxConns(t *testing.T) {
	t.Parallel()

	cfg, err := NewPoolConfig("postgres://onefortythree:dev@localhost:5432/onefortythree?sslmode=disable", PoolOptions{
		MaxConns: 3,
	})
	require.NoError(t, err, "NewPoolConfig should parse a valid database URL")
	require.Equal(t, int32(3), cfg.MaxConns, "NewPoolConfig should apply the configured max connection budget")
}

func TestNewPoolConfigLeavesDefaultMaxConnsWhenUnset(t *testing.T) {
	t.Parallel()

	cfg, err := NewPoolConfig("postgres://onefortythree:dev@localhost:5432/onefortythree?sslmode=disable", PoolOptions{})
	require.NoError(t, err, "NewPoolConfig should parse a valid database URL")
	require.Greater(t, cfg.MaxConns, int32(0), "NewPoolConfig should preserve pgxpool's positive default max connection budget when unset")
}
