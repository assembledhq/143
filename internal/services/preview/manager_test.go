package preview

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestComputeConfigDigest(t *testing.T) {
	t.Parallel()

	cfg := &models.PreviewConfig{
		Version: "3",
		Name:    "my-app",
		Primary: "web",
		Services: map[string]models.ServiceConfig{
			"web": {Port: 3000},
		},
	}

	digest := computeConfigDigest(cfg)
	require.True(t, len(digest) > 0)
	require.Contains(t, digest, "sha256:")

	// Same config produces same digest.
	digest2 := computeConfigDigest(cfg)
	require.Equal(t, digest, digest2)

	// Different config produces different digest.
	cfg2 := &models.PreviewConfig{
		Version: "3",
		Name:    "other-app",
		Primary: "api",
		Services: map[string]models.ServiceConfig{
			"api": {Port: 4000},
		},
	}
	digest3 := computeConfigDigest(cfg2)
	require.NotEqual(t, digest, digest3)
}

func TestGenerateAndHashToken(t *testing.T) {
	t.Parallel()

	token1 := generateToken()
	token2 := generateToken()

	require.Len(t, token1, 64) // 32 bytes → 64 hex chars
	require.NotEqual(t, token1, token2)

	hash1 := hashToken(token1)
	hash2 := hashToken(token1)
	require.Equal(t, hash1, hash2, "same token should produce same hash")

	hash3 := hashToken(token2)
	require.NotEqual(t, hash1, hash3, "different tokens should produce different hashes")
}

func TestNewManager_Defaults(t *testing.T) {
	t.Parallel()

	m := NewManager(ManagerConfig{})
	require.Equal(t, DefaultMaxPreviewsPerUser, m.maxPerUser)
	require.Equal(t, DefaultMaxPreviewsPerOrg, m.maxPerOrg)
	require.Equal(t, DefaultMaxPreviewsPerWorker, m.maxPerWorker)
}

func TestNewManager_CustomCaps(t *testing.T) {
	t.Parallel()

	m := NewManager(ManagerConfig{
		MaxPerUser:   10,
		MaxPerOrg:    20,
		MaxPerWorker: 5,
	})
	require.Equal(t, 10, m.maxPerUser)
	require.Equal(t, 20, m.maxPerOrg)
	require.Equal(t, 5, m.maxPerWorker)
}
