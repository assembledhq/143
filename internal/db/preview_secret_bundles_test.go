package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/crypto"
	"github.com/assembledhq/143/internal/models"
)

var previewSecretBundleCols = []string{"id", "org_id", "name", "encrypted_env", "created_by", "created_at", "updated_at"}

func TestPreviewSecretBundleStore_UpsertEncryptsAndScopesByOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "creating mock pool should not error")
	defer mock.Close()

	orgID := uuid.New()
	userID := uuid.New()
	store := NewPreviewSecretBundleStore(mock, nil)
	env := map[string]string{"DATABASE_URL": "postgres://preview", "STRIPE_SECRET_KEY": "sk_test"}

	mock.ExpectExec("INSERT INTO preview_secret_bundles").
		WithArgs(orgID, "repo-staging", pgxmock.AnyArg(), userID).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	err = store.UpsertEnv(context.Background(), orgID, userID, "repo-staging", env)
	require.NoError(t, err, "UpsertEnv should save a valid bundle")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewSecretBundleStore_GetEnvDecryptsEnv(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "creating mock pool should not error")
	defer mock.Close()

	orgID := uuid.New()
	bundleID := uuid.New()
	userID := uuid.New()
	now := time.Now()
	env := map[string]string{"DATABASE_URL": "postgres://preview"}
	plaintext, err := json.Marshal(env)
	require.NoError(t, err, "fixture env should marshal")

	store := NewPreviewSecretBundleStore(mock, nil)
	mock.ExpectQuery("SELECT .* FROM preview_secret_bundles WHERE org_id").
		WithArgs(orgID, "repo-staging").
		WillReturnRows(pgxmock.NewRows(previewSecretBundleCols).
			AddRow(bundleID, orgID, "repo-staging", crypto.DevEncrypt(plaintext), &userID, now, now))

	got, err := store.GetEnv(context.Background(), orgID, "repo-staging")
	require.NoError(t, err, "GetEnv should decrypt a saved bundle")
	require.Equal(t, env, got, "GetEnv should return the decrypted environment values")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewSecretBundleStore_ListSummariesHidesValues(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "creating mock pool should not error")
	defer mock.Close()

	orgID := uuid.New()
	bundleID := uuid.New()
	userID := uuid.New()
	now := time.Now()
	plaintext, err := json.Marshal(map[string]string{"STRIPE_SECRET_KEY": "sk_test", "DATABASE_URL": "postgres://preview"})
	require.NoError(t, err, "fixture env should marshal")

	store := NewPreviewSecretBundleStore(mock, nil)
	mock.ExpectQuery("SELECT .* FROM preview_secret_bundles WHERE org_id").
		WithArgs(orgID).
		WillReturnRows(pgxmock.NewRows(previewSecretBundleCols).
			AddRow(bundleID, orgID, "repo-staging", crypto.DevEncrypt(plaintext), &userID, now, now))

	got, err := store.ListSummaries(context.Background(), orgID)
	require.NoError(t, err, "ListSummaries should decrypt bundles to derive env names")
	require.Equal(t, []models.PreviewSecretBundleSummary{{
		ID:        bundleID,
		Name:      "repo-staging",
		EnvNames:  []string{"DATABASE_URL", "STRIPE_SECRET_KEY"},
		CreatedBy: &userID,
		CreatedAt: now,
		UpdatedAt: now,
	}}, got, "ListSummaries should return sorted env names without secret values")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewSecretBundleInputValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   models.PreviewSecretBundleInput
		wantErr bool
	}{
		{
			name:  "valid",
			input: models.PreviewSecretBundleInput{Name: "repo-staging", Env: map[string]string{"DATABASE_URL": "postgres://preview"}},
		},
		{
			name:    "blank name",
			input:   models.PreviewSecretBundleInput{Name: " ", Env: map[string]string{"DATABASE_URL": "postgres://preview"}},
			wantErr: true,
		},
		{
			name:    "invalid name",
			input:   models.PreviewSecretBundleInput{Name: "repo/staging", Env: map[string]string{"DATABASE_URL": "postgres://preview"}},
			wantErr: true,
		},
		{
			name:    "invalid env var name",
			input:   models.PreviewSecretBundleInput{Name: "repo-staging", Env: map[string]string{"database-url": "postgres://preview"}},
			wantErr: true,
		},
		{
			name:    "empty env",
			input:   models.PreviewSecretBundleInput{Name: "repo-staging", Env: map[string]string{}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.input.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should reject invalid preview secret bundle input")
				return
			}
			require.NoError(t, err, "Validate should accept valid preview secret bundle input")
		})
	}
}
