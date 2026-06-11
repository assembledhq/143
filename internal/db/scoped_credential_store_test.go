// Package db — scoped_credential_store_test.go
//
// Unit coverage for the row-mapping helper and the constructor's nil-guard.
// The adapter's routing behavior at the SQL layer is exercised end-to-end
// through the auth-service tests in internal/services/{codexauth,
// claudecodeauth}, which run against a scope-aware in-memory mock that
// mirrors the real partitioning.
package db

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestScopedCredentialStore_DecryptedCredentialFromCoding(t *testing.T) {
	// The unified row's provider name and config must pass through
	// untranslated — the OAuth services speak the unified vocabulary
	// (anthropic_subscription / AnthropicSubscriptionConfig) directly.
	t.Parallel()

	expiry := time.Now().Add(time.Hour).UTC()
	created := time.Now().UTC()
	row := &models.DecryptedCodingCredential{
		ID:        uuid.New(),
		OrgID:     uuid.New(),
		UserID:    uuidPtr(uuid.New()),
		Provider:  models.ProviderAnthropicSubscription,
		Label:     "personal-claude",
		Status:    models.CodingCredentialStatusActive,
		Priority:  1,
		CreatedAt: created,
		UpdatedAt: created,
		Config: models.AnthropicSubscriptionConfig{
			AccessToken:  "access",
			RefreshToken: "refresh",
			ExpiresAt:    expiry,
		},
	}
	flat, err := decryptedCredentialFromCoding(row)
	require.NoError(t, err)
	require.Equal(t, models.ProviderAnthropicSubscription, flat.Provider, "provider must pass through untranslated")
	require.Equal(t, row.ID, flat.ID)
	require.Equal(t, row.OrgID, flat.OrgID)
	require.Equal(t, row.Label, flat.Label)
	require.Equal(t, models.CredentialStatus(row.Status), flat.Status)

	cfg, ok := flat.Config.(models.AnthropicSubscriptionConfig)
	require.True(t, ok, "config must pass through as AnthropicSubscriptionConfig, got %T", flat.Config)
	require.Equal(t, "access", cfg.AccessToken)
	require.Equal(t, "refresh", cfg.RefreshToken)
	require.True(t, expiry.Equal(cfg.ExpiresAt))
}

func TestScopedCredentialStore_DecryptedCredentialFromCodingNilRow(t *testing.T) {
	t.Parallel()

	_, err := decryptedCredentialFromCoding(nil)
	require.Error(t, err, "nil rows should surface an error instead of a panic")
}

func TestScopedCredentialStore_NewPanicsOnNilCodingStore(t *testing.T) {
	// Construction-time guard: missing wiring fails fast at boot rather
	// than surfacing as cryptic per-request errors.
	t.Parallel()

	require.PanicsWithValue(t,
		"db: NewScopedCredentialStore requires a non-nil CodingCredentialStore",
		func() { NewScopedCredentialStore(nil) },
	)
}

func uuidPtr(id uuid.UUID) *uuid.UUID { return &id }
