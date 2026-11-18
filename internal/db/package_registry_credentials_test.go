package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func packageRegistryTestRow() models.PackageRegistryCredential {
	return models.PackageRegistryCredential{PackageRegistryCredentialSummary: models.PackageRegistryCredentialSummary{ID: uuid.New(), RepositoryID: uuid.New(), RegistryURL: "https://registry.example.test/", PackageScope: "@example", Revision: uuid.New(), CreatedAt: time.Unix(10, 0), UpdatedAt: time.Unix(20, 0)}, OrgID: uuid.New(), TokenEncrypted: []byte("synthetic-ciphertext")}
}
func packageRegistryTestRows(row models.PackageRegistryCredential) *pgxmock.Rows {
	return pgxmock.NewRows([]string{"id", "repository_id", "registry_url", "package_scope", "revision", "created_at", "updated_at"}).AddRow(row.ID, row.RepositoryID, row.RegistryURL, row.PackageScope, row.Revision, row.CreatedAt, row.UpdatedAt)
}

func TestPackageRegistryCredentialStoreScoping(t *testing.T) {
	t.Parallel()
	tests := []string{"repository", "metadata", "get", "encrypted", "create", "rotate", "delete"}
	for _, operation := range tests {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "database fixture should initialize")
			defer mock.Close()
			row := packageRegistryTestRow()
			store := NewPackageRegistryCredentialStore(mock)
			ctx := context.Background()
			scope := pgx.NamedArgs{"org_id": row.OrgID, "repository_id": row.RepositoryID}
			switch operation {
			case "repository":
				mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM repositories WHERE org_id = @org_id AND id = @repository_id\)`).WithArgs(scope).WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
				exists, err := store.RepositoryExists(ctx, row.OrgID, row.RepositoryID)
				require.NoError(t, err, "repository ownership lookup should succeed")
				require.True(t, exists, "owned repository should exist")
			case "metadata":
				mock.ExpectQuery(`SELECT id, repository_id, registry_url, package_scope, revision, created_at, updated_at FROM package_registry_credentials WHERE org_id = @org_id AND repository_id = @repository_id ORDER BY package_scope, id`).WithArgs(scope).WillReturnRows(packageRegistryTestRows(row))
				actual, err := store.ListMetadata(ctx, row.OrgID, row.RepositoryID)
				require.NoError(t, err, "metadata lookup should succeed")
				require.Equal(t, []models.PackageRegistryCredentialSummary{row.PackageRegistryCredentialSummary}, actual, "metadata must use the exact org and repository without selecting ciphertext")
			case "get":
				scope["id"] = row.ID
				mock.ExpectQuery(`FROM package_registry_credentials WHERE org_id = @org_id AND repository_id = @repository_id AND id = @id`).WithArgs(scope).WillReturnRows(packageRegistryTestRows(row))
				actual, err := store.GetMetadata(ctx, row.OrgID, row.RepositoryID, row.ID)
				require.NoError(t, err, "credential lookup should succeed")
				require.Equal(t, row.PackageRegistryCredentialSummary, actual, "credential lookup must scope org, repository and credential")
			case "encrypted":
				mock.ExpectQuery(`SELECT id, repository_id, registry_url, package_scope, revision, created_at, updated_at, org_id, token_encrypted FROM package_registry_credentials WHERE org_id = @org_id AND repository_id = @repository_id ORDER BY package_scope, id`).WithArgs(scope).WillReturnRows(pgxmock.NewRows([]string{"id", "repository_id", "registry_url", "package_scope", "revision", "created_at", "updated_at", "org_id", "token_encrypted"}).AddRow(row.ID, row.RepositoryID, row.RegistryURL, row.PackageScope, row.Revision, row.CreatedAt, row.UpdatedAt, row.OrgID, row.TokenEncrypted))
				actual, err := store.ListEncrypted(ctx, row.OrgID, row.RepositoryID)
				require.NoError(t, err, "encrypted lookup should succeed")
				require.Equal(t, []models.PackageRegistryCredential{row}, actual, "resolver lookup must retain scoped identity and encrypted material")
			case "create":
				for key, value := range (pgx.NamedArgs{"id": row.ID, "registry_url": row.RegistryURL, "package_scope": row.PackageScope, "revision": row.Revision, "token_encrypted": row.TokenEncrypted}) {
					scope[key] = value
				}
				mock.ExpectQuery(`INSERT INTO package_registry_credentials .* SELECT .* FROM repositories r WHERE r.org_id = @org_id AND r.id = @repository_id RETURNING`).WithArgs(scope).WillReturnRows(packageRegistryTestRows(row))
				actual, err := store.Create(ctx, row.OrgID, row)
				require.NoError(t, err, "credential insertion should succeed")
				require.Equal(t, row.PackageRegistryCredentialSummary, actual, "creation must validate repository ownership before storing ciphertext")
			case "rotate":
				revision := uuid.New()
				for key, value := range (pgx.NamedArgs{"id": row.ID, "revision": row.Revision, "expected_revision": revision, "token_encrypted": row.TokenEncrypted}) {
					scope[key] = value
				}
				mock.ExpectQuery(`UPDATE package_registry_credentials SET token_encrypted = @token_encrypted, revision = @revision, updated_at = now\(\) WHERE org_id = @org_id AND repository_id = @repository_id AND id = @id AND revision = @expected_revision RETURNING`).WithArgs(scope).WillReturnRows(packageRegistryTestRows(row))
				actual, err := store.Rotate(ctx, row.OrgID, row, revision)
				require.NoError(t, err, "current credential rotation should succeed")
				require.Equal(t, row.PackageRegistryCredentialSummary, actual, "rotation must atomically replace ciphertext only for the expected revision")
			case "delete":
				scope["id"], scope["expected_revision"] = row.ID, row.Revision
				mock.ExpectExec(`DELETE FROM package_registry_credentials WHERE org_id = @org_id AND repository_id = @repository_id AND id = @id AND revision = @expected_revision`).WithArgs(scope).WillReturnResult(pgxmock.NewResult("DELETE", 1))
				require.NoError(t, store.Delete(ctx, row.OrgID, row.RepositoryID, row.ID, row.Revision), "deletion must remove current scoped ciphertext")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "queries must include every tenant and revision predicate")
		})
	}
}

func TestPackageRegistryCredentialStoreConflicts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, operation string
		wantErr         error
		dbErr           error
	}{
		{name: "scope already configured", operation: "create", wantErr: ErrPackageRegistryScopeConflict, dbErr: &pgconn.PgError{Code: "23505", ConstraintName: "package_registry_credentials_scope_unique"}},
		{name: "registry already configured", operation: "create", wantErr: ErrPackageRegistryScopeConflict, dbErr: &pgconn.PgError{Code: "23505", ConstraintName: "package_registry_credentials_origin_unique"}},
		{name: "repository belongs to another org", operation: "create", wantErr: pgx.ErrNoRows},
		{name: "credential belongs to another repository", operation: "get", wantErr: pgx.ErrNoRows},
		{name: "concurrent rotation", operation: "rotate", wantErr: ErrPackageRegistryCredentialConflict},
		{name: "concurrent deletion", operation: "delete", wantErr: ErrPackageRegistryCredentialConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "database fixture should initialize")
			defer mock.Close()
			store := NewPackageRegistryCredentialStore(mock)
			row := packageRegistryTestRow()
			empty := pgxmock.NewRows([]string{"id", "repository_id", "registry_url", "package_scope", "revision", "created_at", "updated_at"})
			switch tt.operation {
			case "create":
				query := mock.ExpectQuery("INSERT INTO package_registry_credentials").WithArgs(pgx.NamedArgs{"org_id": row.OrgID, "repository_id": row.RepositoryID, "id": row.ID, "registry_url": row.RegistryURL, "package_scope": row.PackageScope, "revision": row.Revision, "token_encrypted": row.TokenEncrypted})
				if tt.dbErr != nil {
					query.WillReturnError(tt.dbErr)
				} else {
					query.WillReturnRows(empty)
				}
				_, err = store.Create(context.Background(), row.OrgID, row)
			case "get":
				mock.ExpectQuery("SELECT .* FROM package_registry_credentials WHERE org_id = @org_id AND repository_id = @repository_id AND id = @id").WithArgs(pgx.NamedArgs{"org_id": row.OrgID, "repository_id": row.RepositoryID, "id": row.ID}).WillReturnRows(empty)
				_, err = store.GetMetadata(context.Background(), row.OrgID, row.RepositoryID, row.ID)
			case "rotate":
				mock.ExpectQuery("UPDATE package_registry_credentials").WithArgs(pgx.NamedArgs{"org_id": row.OrgID, "repository_id": row.RepositoryID, "id": row.ID, "revision": row.Revision, "expected_revision": row.Revision, "token_encrypted": row.TokenEncrypted}).WillReturnRows(empty)
				_, err = store.Rotate(context.Background(), row.OrgID, row, row.Revision)
			case "delete":
				mock.ExpectExec("DELETE FROM package_registry_credentials").WithArgs(pgx.NamedArgs{"org_id": row.OrgID, "repository_id": row.RepositoryID, "id": row.ID, "expected_revision": row.Revision}).WillReturnResult(pgxmock.NewResult("DELETE", 0))
				err = store.Delete(context.Background(), row.OrgID, row.RepositoryID, row.ID, row.Revision)
			}
			require.ErrorIs(t, err, tt.wantErr, "conflicts and unavailable scopes must be safe and distinguishable")
			require.NoError(t, mock.ExpectationsWereMet(), "store should complete only the expected operation")
		})
	}
}
