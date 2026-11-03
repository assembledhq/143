package packagecredentials

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/crypto"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type credentialStoreFake struct {
	exists              bool
	rows                []models.PackageRegistryCredential
	metadata            []models.PackageRegistryCredentialSummary
	captured            models.PackageRegistryCredential
	orgID, repositoryID uuid.UUID
	encryptedReads      int
	rotated, deleted    bool
}

func (s *credentialStoreFake) RepositoryExists(_ context.Context, orgID, repositoryID uuid.UUID) (bool, error) {
	s.orgID, s.repositoryID = orgID, repositoryID
	return s.exists, nil
}
func (s *credentialStoreFake) ListMetadata(_ context.Context, orgID, repositoryID uuid.UUID) ([]models.PackageRegistryCredentialSummary, error) {
	s.orgID, s.repositoryID = orgID, repositoryID
	return s.metadata, nil
}
func (s *credentialStoreFake) GetMetadata(_ context.Context, orgID, repositoryID, id uuid.UUID) (models.PackageRegistryCredentialSummary, error) {
	s.orgID, s.repositoryID = orgID, repositoryID
	if len(s.metadata) == 0 {
		return models.PackageRegistryCredentialSummary{}, pgx.ErrNoRows
	}
	return s.metadata[0], nil
}
func (s *credentialStoreFake) Create(_ context.Context, orgID uuid.UUID, row models.PackageRegistryCredential) (models.PackageRegistryCredentialSummary, error) {
	s.orgID = orgID
	s.captured = row
	return row.PackageRegistryCredentialSummary, nil
}
func (s *credentialStoreFake) Rotate(_ context.Context, orgID uuid.UUID, row models.PackageRegistryCredential, _ uuid.UUID) (models.PackageRegistryCredentialSummary, error) {
	s.orgID = orgID
	s.captured = row
	s.rotated = true
	return row.PackageRegistryCredentialSummary, nil
}
func (s *credentialStoreFake) Delete(_ context.Context, orgID, repositoryID, _, _ uuid.UUID) error {
	s.orgID, s.repositoryID = orgID, repositoryID
	s.deleted = true
	return nil
}
func (s *credentialStoreFake) ListEncrypted(_ context.Context, orgID, repositoryID uuid.UUID) ([]models.PackageRegistryCredential, error) {
	s.orgID, s.repositoryID = orgID, repositoryID
	s.encryptedReads++
	return s.rows, nil
}

func newCredentialCrypto(t *testing.T) *crypto.Service {
	t.Helper()
	svc, err := crypto.NewService(strings.Repeat("synthetic-key", 4))
	require.NoError(t, err, "test encryption should initialize")
	return svc
}

func TestPackageRegistryCredentialRoundTripAndBinding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		alter   func(*models.PackageRegistryCredential)
		wantErr error
	}{
		{name: "valid"},
		{name: "different org", alter: func(row *models.PackageRegistryCredential) { row.OrgID = uuid.New() }, wantErr: ErrCredentialBinding},
		{name: "different repository", alter: func(row *models.PackageRegistryCredential) { row.RepositoryID = uuid.New() }, wantErr: ErrCredentialBinding},
		{name: "different credential", alter: func(row *models.PackageRegistryCredential) { row.ID = uuid.New() }, wantErr: ErrCredentialBinding},
		{name: "different revision", alter: func(row *models.PackageRegistryCredential) { row.Revision = uuid.New() }, wantErr: ErrCredentialBinding},
		{name: "different registry", alter: func(row *models.PackageRegistryCredential) { row.RegistryURL = "https://other.example.test/" }, wantErr: ErrCredentialBinding},
		{name: "different scope", alter: func(row *models.PackageRegistryCredential) { row.PackageScope = "@different" }, wantErr: ErrCredentialBinding},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			orgID, repoID := uuid.New(), uuid.New()
			store := &credentialStoreFake{exists: true}
			svc := NewService(store, newCredentialCrypto(t))
			created, err := svc.Create(ctx, orgID, repoID, "https://Registry.Example.test:443", "@example", "synthetic-token")
			require.NoError(t, err, "credential should be stored encrypted")
			require.Equal(t, "https://registry.example.test/", created.RegistryURL, "registry origin should be canonical")
			require.NotContains(t, string(store.captured.TokenEncrypted), "synthetic-token", "ciphertext must not contain the token")
			row := store.captured
			if tt.alter != nil {
				tt.alter(&row)
			}
			store.rows = []models.PackageRegistryCredential{row}
			resolved, err := svc.Resolve(ctx, orgID, repoID)
			require.ErrorIs(t, err, tt.wantErr, "resolve should enforce every encrypted identity field")
			if tt.wantErr == nil {
				require.Equal(t, []models.ResolvedPackageRegistryCredential{{Metadata: created, Token: "synthetic-token"}}, resolved, "resolution should return only the original scoped credential")
			}
			serialized, err := json.Marshal(store.captured)
			require.NoError(t, err, "internal credential should serialize safely")
			require.NotContains(t, string(serialized), "token", "internal serialization must omit secret material")
		})
	}
}

func TestPackageRegistryMetadataDoesNotDecrypt(t *testing.T) {
	t.Parallel()
	orgID, repoID := uuid.New(), uuid.New()
	expected := []models.PackageRegistryCredentialSummary{{ID: uuid.New(), RepositoryID: repoID, RegistryURL: "https://registry.example.test/", PackageScope: "@example", Revision: uuid.New(), CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(2, 0)}}
	store := &credentialStoreFake{exists: true, metadata: expected}
	actual, err := NewService(store, nil).Metadata(context.Background(), orgID, repoID)
	require.NoError(t, err, "metadata should work without an encryption key")
	require.Equal(t, expected, actual, "metadata should return the exact summaries")
	require.Equal(t, 0, store.encryptedReads, "metadata must not read encrypted tokens")
	require.Equal(t, orgID, store.orgID, "metadata must preserve the active org scope")
	require.Equal(t, repoID, store.repositoryID, "metadata must preserve the repository scope")
}

func TestPackageRegistryMutationsAndEncryption(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		key          bool
		stale        bool
		op           string
		wantErr      error
		wantMutation bool
	}{
		{name: "create needs encryption", op: "create", wantErr: ErrEncryptionUnavailable},
		{name: "rotate needs encryption", op: "rotate", wantErr: ErrEncryptionUnavailable},
		{name: "stale rotation rejected", op: "rotate", key: true, stale: true, wantErr: db.ErrPackageRegistryCredentialConflict},
		{name: "rotation changes revision", op: "rotate", key: true, wantMutation: true},
		{name: "stale deletion rejected", op: "delete", stale: true, wantErr: db.ErrPackageRegistryCredentialConflict},
		{name: "delete without encryption", op: "delete", wantMutation: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			orgID, repoID, id, revision := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			store := &credentialStoreFake{exists: true, metadata: []models.PackageRegistryCredentialSummary{{ID: id, RepositoryID: repoID, RegistryURL: "https://registry.example.test/", PackageScope: "@example", Revision: revision}}}
			var key *crypto.Service
			if tt.key {
				key = newCredentialCrypto(t)
			}
			svc := NewService(store, key)
			expectedRevision := revision
			if tt.stale {
				expectedRevision = uuid.New()
			}
			var err error
			switch tt.op {
			case "create":
				_, err = svc.Create(ctx, orgID, repoID, "https://registry.example.test/", "@example", "synthetic-token")
			case "rotate":
				_, err = svc.Rotate(ctx, orgID, repoID, id, expectedRevision, "synthetic-replacement")
			case "delete":
				err = svc.Delete(ctx, orgID, repoID, id, expectedRevision)
			}
			require.ErrorIs(t, err, tt.wantErr, "mutation should enforce revision and encryption requirements")
			require.Equal(t, tt.wantMutation, store.rotated || store.deleted, "only authorized current mutations should reach the store")
			if store.rotated {
				require.NotEqual(t, revision, store.captured.Revision, "rotation must invalidate cached setup receipts")
				store.rows = []models.PackageRegistryCredential{store.captured}
				actual, err := svc.Resolve(ctx, orgID, repoID)
				require.NoError(t, err, "rotated credential should decrypt with its new binding")
				require.Equal(t, "synthetic-replacement", actual[0].Token, "resolution should use the replacement token")
			}
		})
	}
}

func TestPackageRegistryValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, url, scope, token string
		valid                   bool
	}{
		{"valid", "https://registry.example.test/", "@example", "synthetic-token", true},
		{"http", "http://registry.example.test/", "@example", "synthetic", false},
		{"userinfo", "https://user:password@registry.example.test/", "@example", "synthetic", false},
		{"path", "https://registry.example.test/private", "@example", "synthetic", false},
		{"escaped path", "https://registry.example.test/%2f", "@example", "synthetic", false},
		{"query", "https://registry.example.test/?token=synthetic", "@example", "synthetic", false},
		{"fragment", "https://registry.example.test/#synthetic", "@example", "synthetic", false},
		{"port", "https://registry.example.test:8443/", "@example", "synthetic", false},
		{"ip", "https://127.0.0.1/", "@example", "synthetic", false},
		{"invalid dns", "https://invalid..example.test/", "@example", "synthetic", false},
		{"no scope", "https://registry.example.test/", "", "synthetic", false},
		{"uppercase scope", "https://registry.example.test/", "@Example", "synthetic", false},
		{"scope injection", "https://registry.example.test/", "@example\nregistry=bad", "synthetic", false},
		{"token injection", "https://registry.example.test/", "@example", "synthetic\nregistry=bad", false},
		{"empty token", "https://registry.example.test/", "@example", "", false},
		{"token whitespace", "https://registry.example.test/", "@example", "synthetic token", false},
		{"large token", "https://registry.example.test/", "@example", strings.Repeat("x", 8193), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := &credentialStoreFake{exists: true}
			_, err := NewService(store, newCredentialCrypto(t)).Create(context.Background(), uuid.New(), uuid.New(), tt.url, tt.scope, tt.token)
			if tt.valid {
				require.NoError(t, err, "valid scoped registry credentials should be accepted")
			} else {
				require.ErrorIs(t, err, ErrInvalidCredential, "invalid config must not reach encryption/storage")
				require.Equal(t, uuid.Nil, store.captured.ID, "invalid inputs must not write credentials")
				require.NotContains(t, err.Error(), tt.token+"\"", "validation errors must not echo secret input")
			}
		})
	}
}

func TestPackageRegistryUnavailableRepository(t *testing.T) {
	t.Parallel()
	tests := []string{"missing repository", "missing org"}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			orgID := uuid.New()
			if name == "missing org" {
				orgID = uuid.Nil
			}
			svc := NewService(&credentialStoreFake{}, newCredentialCrypto(t))
			_, err := svc.Create(context.Background(), orgID, uuid.New(), "https://registry.example.test/", "@example", "synthetic-token")
			require.True(t, errors.Is(err, pgx.ErrNoRows), "missing scope should be indistinguishable from a missing repository")
		})
	}
}
