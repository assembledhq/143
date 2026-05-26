package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestPreviewSecretBundleStore_UpsertEncryptsAndVersions(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "creating mock pool should not error")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	userID := uuid.New()
	bundleID := uuid.New()
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_secret_bundles").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "repository_id": repoID, "name": "repo-dev"}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO preview_secret_bundles").
		WithArgs(pgx.NamedArgs{
			"org_id":                   orgID,
			"repository_id":            repoID,
			"name":                     "repo-dev",
			"source_type":              "managed",
			"source_config_encrypted":  pgxmock.AnyArg(),
			"outputs_config_encrypted": pgxmock.AnyArg(),
			"exposure_policy":          "preview_runtime",
			"created_by_user_id":       userID,
		}).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repository_id", "name", "active", "source_type", "source_config_encrypted",
			"outputs_config_encrypted", "exposure_policy", "created_by_user_id", "created_at",
		}).AddRow(
			bundleID, orgID, repoID, "repo-dev", true, "managed", []byte(`{"alg":"dev-plaintext","ciphertext":"djA6e30="}`),
			[]byte(`{"alg":"dev-plaintext","ciphertext":"djA6W10="}`), "preview_runtime", userID, time.Now(),
		))
	mock.ExpectCommit()

	store := NewPreviewSecretBundleStore(mock, nil, "test-key")
	row, err := store.Upsert(context.Background(), orgID, UpsertPreviewSecretBundleInput{
		RepositoryID:    repoID,
		Name:            "repo-dev",
		Source:          models.PreviewSecretBundleSource{Type: "managed", Values: map[string]string{"DATABASE_URL": "postgres://"}},
		Outputs:         []models.PreviewSecretBundleOutput{{Type: "env", Values: map[string]string{"DATABASE_URL": "secret:DATABASE_URL"}}},
		CreatedByUserID: userID,
	})

	require.NoError(t, err, "Upsert should insert a new active bundle version")
	require.Equal(t, bundleID, row.ID, "Upsert should return the inserted bundle row")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewSecretBundleStore_DisableInsertsInactiveSuccessor(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "creating mock pool should not error")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	userID := uuid.New()
	activeID := uuid.New()
	inactiveID := uuid.New()
	now := time.Now()
	sourceEncrypted := json.RawMessage(`{"alg":"dev-plaintext","ciphertext":"djA6e30="}`)
	outputsEncrypted := json.RawMessage(`{"alg":"dev-plaintext","ciphertext":"djA6W10="}`)

	mock.ExpectBegin()
	mock.ExpectQuery("FROM preview_secret_bundles").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "repository_id": repoID, "name": "repo-dev"}).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repository_id", "name", "active", "source_type", "source_config_encrypted",
			"outputs_config_encrypted", "exposure_policy", "created_by_user_id", "created_at",
		}).AddRow(
			activeID, orgID, repoID, "repo-dev", true, "managed", sourceEncrypted,
			outputsEncrypted, "preview_runtime", userID, now,
		))
	mock.ExpectExec("UPDATE preview_secret_bundles").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "id": activeID}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO preview_secret_bundles").
		WithArgs(pgx.NamedArgs{
			"org_id":                   orgID,
			"repository_id":            repoID,
			"name":                     "repo-dev",
			"active":                   false,
			"source_type":              "managed",
			"source_config_encrypted":  sourceEncrypted,
			"outputs_config_encrypted": outputsEncrypted,
			"exposure_policy":          "preview_runtime",
			"created_by_user_id":       userID,
		}).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repository_id", "name", "active", "source_type", "source_config_encrypted",
			"outputs_config_encrypted", "exposure_policy", "created_by_user_id", "created_at",
		}).AddRow(
			inactiveID, orgID, repoID, "repo-dev", false, "managed", sourceEncrypted,
			outputsEncrypted, "preview_runtime", userID, now,
		))
	mock.ExpectCommit()

	store := NewPreviewSecretBundleStore(mock, nil, "test-key")
	err = store.Disable(context.Background(), orgID, repoID, "repo-dev", userID)

	require.NoError(t, err, "Disable should preserve an inactive successor version")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPreviewSecretBundleStore_ReplaceActiveByIDRejectsRenameConflict(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "creating mock pool should not error")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	userID := uuid.New()
	existingID := uuid.New()
	conflictingID := uuid.New()
	now := time.Now()
	sourceEncrypted := json.RawMessage(`{"alg":"dev-plaintext","ciphertext":"djA6e30="}`)
	outputsEncrypted := json.RawMessage(`{"alg":"dev-plaintext","ciphertext":"djA6W10="}`)
	columns := []string{
		"id", "org_id", "repository_id", "name", "active", "source_type", "source_config_encrypted",
		"outputs_config_encrypted", "exposure_policy", "created_by_user_id", "created_at",
	}

	mock.ExpectBegin()
	mock.ExpectQuery("FROM preview_secret_bundles").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "id": existingID}).
		WillReturnRows(pgxmock.NewRows(columns).AddRow(
			existingID, orgID, repoID, "repo-dev", true, "managed", sourceEncrypted,
			outputsEncrypted, "preview_runtime", userID, now,
		))
	mock.ExpectQuery("FROM preview_secret_bundles").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "repository_id": repoID, "name": "repo-prod"}).
		WillReturnRows(pgxmock.NewRows(columns).AddRow(
			conflictingID, orgID, repoID, "repo-prod", true, "managed", sourceEncrypted,
			outputsEncrypted, "preview_runtime", userID, now,
		))
	mock.ExpectRollback()

	store := NewPreviewSecretBundleStore(mock, nil, "test-key")
	_, err = store.ReplaceActiveByID(context.Background(), orgID, existingID, UpsertPreviewSecretBundleInput{
		RepositoryID:    repoID,
		Name:            "repo-prod",
		Source:          models.PreviewSecretBundleSource{Type: "managed", Values: map[string]string{"DATABASE_URL": "postgres://"}},
		Outputs:         []models.PreviewSecretBundleOutput{{Type: "env", Values: map[string]string{"DATABASE_URL": "secret:DATABASE_URL"}}},
		CreatedByUserID: userID,
	})

	require.ErrorIs(t, err, ErrPreviewSecretBundleNameConflict, "ReplaceActiveByID should reject renaming over another active bundle")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
