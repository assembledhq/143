package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const packageRegistryCredentialSummaryColumns = "id, repository_id, registry_url, package_scope, revision, created_at, updated_at"

var ErrPackageRegistryCredentialConflict = errors.New("package registry credential changed")
var ErrPackageRegistryScopeConflict = errors.New("registry origin or package scope already configured")

type PackageRegistryCredentialStore struct{ db DBTX }

func NewPackageRegistryCredentialStore(db DBTX) *PackageRegistryCredentialStore {
	return &PackageRegistryCredentialStore{db: db}
}

func (s *PackageRegistryCredentialStore) RepositoryExists(ctx context.Context, orgID, repositoryID uuid.UUID) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM repositories WHERE org_id = @org_id AND id = @repository_id)`, pgx.NamedArgs{"org_id": orgID, "repository_id": repositoryID}).Scan(&exists)
	return exists, err
}

func (s *PackageRegistryCredentialStore) ListMetadata(ctx context.Context, orgID, repositoryID uuid.UUID) ([]models.PackageRegistryCredentialSummary, error) {
	rows, err := s.db.Query(ctx, `SELECT `+packageRegistryCredentialSummaryColumns+` FROM package_registry_credentials WHERE org_id = @org_id AND repository_id = @repository_id ORDER BY package_scope, id`, pgx.NamedArgs{"org_id": orgID, "repository_id": repositoryID})
	if err != nil {
		return nil, fmt.Errorf("list package registry credential metadata: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PackageRegistryCredentialSummary])
}

func (s *PackageRegistryCredentialStore) GetMetadata(ctx context.Context, orgID, repositoryID, id uuid.UUID) (models.PackageRegistryCredentialSummary, error) {
	rows, err := s.db.Query(ctx, `SELECT `+packageRegistryCredentialSummaryColumns+` FROM package_registry_credentials WHERE org_id = @org_id AND repository_id = @repository_id AND id = @id`, pgx.NamedArgs{"org_id": orgID, "repository_id": repositoryID, "id": id})
	if err != nil {
		return models.PackageRegistryCredentialSummary{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PackageRegistryCredentialSummary])
}

func (s *PackageRegistryCredentialStore) Create(ctx context.Context, orgID uuid.UUID, row models.PackageRegistryCredential) (models.PackageRegistryCredentialSummary, error) {
	rows, err := s.db.Query(ctx, `INSERT INTO package_registry_credentials (id, org_id, repository_id, registry_url, package_scope, revision, token_encrypted)
 SELECT @id, @org_id, r.id, @registry_url, @package_scope, @revision, @token_encrypted FROM repositories r WHERE r.org_id = @org_id AND r.id = @repository_id
 RETURNING `+packageRegistryCredentialSummaryColumns, pgx.NamedArgs{"org_id": orgID, "repository_id": row.RepositoryID, "id": row.ID, "registry_url": row.RegistryURL, "package_scope": row.PackageScope, "revision": row.Revision, "token_encrypted": row.TokenEncrypted})
	if err != nil {
		if isUniqueViolation(err) {
			return models.PackageRegistryCredentialSummary{}, ErrPackageRegistryScopeConflict
		}
		return models.PackageRegistryCredentialSummary{}, err
	}
	result, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PackageRegistryCredentialSummary])
	if isUniqueViolation(err) {
		return models.PackageRegistryCredentialSummary{}, ErrPackageRegistryScopeConflict
	}
	return result, err
}

func (s *PackageRegistryCredentialStore) Rotate(ctx context.Context, orgID uuid.UUID, row models.PackageRegistryCredential, expectedRevision uuid.UUID) (models.PackageRegistryCredentialSummary, error) {
	rows, err := s.db.Query(ctx, `UPDATE package_registry_credentials SET token_encrypted = @token_encrypted, revision = @revision, updated_at = now()
 WHERE org_id = @org_id AND repository_id = @repository_id AND id = @id AND revision = @expected_revision RETURNING `+packageRegistryCredentialSummaryColumns,
		pgx.NamedArgs{"org_id": orgID, "repository_id": row.RepositoryID, "id": row.ID, "revision": row.Revision, "expected_revision": expectedRevision, "token_encrypted": row.TokenEncrypted})
	if err != nil {
		return models.PackageRegistryCredentialSummary{}, err
	}
	result, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PackageRegistryCredentialSummary])
	if errors.Is(err, pgx.ErrNoRows) {
		return models.PackageRegistryCredentialSummary{}, ErrPackageRegistryCredentialConflict
	}
	return result, err
}

func (s *PackageRegistryCredentialStore) Delete(ctx context.Context, orgID, repositoryID, id, expectedRevision uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM package_registry_credentials WHERE org_id = @org_id AND repository_id = @repository_id AND id = @id AND revision = @expected_revision`, pgx.NamedArgs{"org_id": orgID, "repository_id": repositoryID, "id": id, "expected_revision": expectedRevision})
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrPackageRegistryCredentialConflict
	}
	return nil
}

func (s *PackageRegistryCredentialStore) ListEncrypted(ctx context.Context, orgID, repositoryID uuid.UUID) ([]models.PackageRegistryCredential, error) {
	rows, err := s.db.Query(ctx, `SELECT `+packageRegistryCredentialSummaryColumns+`, org_id, token_encrypted FROM package_registry_credentials WHERE org_id = @org_id AND repository_id = @repository_id ORDER BY package_scope, id`, pgx.NamedArgs{"org_id": orgID, "repository_id": repositoryID})
	if err != nil {
		return nil, fmt.Errorf("list encrypted package registry credentials: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PackageRegistryCredential])
}
