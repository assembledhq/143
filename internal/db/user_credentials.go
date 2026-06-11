package db

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/crypto"
	"github.com/assembledhq/143/internal/models"
)

// UserCredentialStore handles encrypted per-user credential storage.
//
// Post unified-coding-credentials cleanup, this store serves exactly one
// consumer: GitHub App user OAuth tokens (provider github_app_user) used for
// user-authored PRs. Personal coding-agent credentials live in
// coding_credentials with user_id set; the team-default concept is gone.
type UserCredentialStore struct {
	db     DBTX
	crypto *crypto.Service
}

// NewUserCredentialStore creates a new user credential store.
func NewUserCredentialStore(db DBTX, cryptoSvc *crypto.Service) *UserCredentialStore {
	return &UserCredentialStore{db: db, crypto: cryptoSvc}
}

// Upsert inserts or replaces the (org, user, provider) credential.
func (s *UserCredentialStore) Upsert(ctx context.Context, userID, orgID uuid.UUID, cfg models.ProviderConfig) error {
	provider := cfg.Provider()

	plaintext, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal %s config: %w", provider, err)
	}

	encrypted, err := s.encrypt(plaintext)
	if err != nil {
		return fmt.Errorf("encrypt %s config: %w", provider, err)
	}

	query := `
		INSERT INTO user_credentials (user_id, org_id, provider, config, status)
		VALUES (@user_id, @org_id, @provider, @config, 'active')
		ON CONFLICT (org_id, user_id, provider)
		DO UPDATE SET config = @config, status = 'active', updated_at = now()
		RETURNING id`

	args := pgx.NamedArgs{
		"user_id":  userID,
		"org_id":   orgID,
		"provider": string(provider),
		"config":   encrypted,
	}

	var id uuid.UUID
	if err := s.db.QueryRow(ctx, query, args).Scan(&id); err != nil {
		return fmt.Errorf("upsert user %s credential: %w", provider, err)
	}
	return nil
}

// GetForUser retrieves a user's credential for a specific provider.
func (s *UserCredentialStore) GetForUser(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) (*models.DecryptedUserCredential, error) {
	query := `
		SELECT id, user_id, org_id, provider, config, status, last_verified_at, created_at, updated_at
		FROM user_credentials
		WHERE org_id = @org_id AND user_id = @user_id AND provider = @provider AND status != 'disabled'`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"user_id":  userID,
		"provider": string(provider),
	})
	if err != nil {
		return nil, fmt.Errorf("query user %s credential: %w", provider, err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.UserCredential])
	if err != nil {
		return nil, fmt.Errorf("get user %s credential: %w", provider, err)
	}

	return s.decryptRow(row)
}

// Disable soft-deletes the (org, user, provider) credential.
func (s *UserCredentialStore) Disable(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) error {
	_, err := s.db.Exec(ctx,
		`UPDATE user_credentials SET status = 'disabled', updated_at = now()
		 WHERE org_id = @org_id AND user_id = @user_id AND provider = @provider`,
		pgx.NamedArgs{"org_id": orgID, "user_id": userID, "provider": string(provider)},
	)
	return err
}

// encrypt handles encryption or dev-mode plaintext marking.
func (s *UserCredentialStore) encrypt(plaintext []byte) ([]byte, error) {
	if s.crypto != nil {
		return s.crypto.Encrypt(plaintext)
	}
	return crypto.DevEncrypt(plaintext), nil
}

// decrypt handles decryption or dev-mode plaintext reading.
func (s *UserCredentialStore) decrypt(data []byte) ([]byte, error) {
	if s.crypto != nil {
		return s.crypto.Decrypt(data)
	}
	return crypto.DevDecrypt(data)
}

// decryptRow decrypts a DB row and parses into a DecryptedUserCredential.
func (s *UserCredentialStore) decryptRow(row models.UserCredential) (*models.DecryptedUserCredential, error) {
	plaintext, err := s.decrypt(row.Config)
	if err != nil {
		return nil, fmt.Errorf("decrypt %s config: %w", row.Provider, err)
	}

	cfg, err := models.ParseProviderConfig(row.Provider, plaintext)
	if err != nil {
		return nil, fmt.Errorf("parse %s config: %w", row.Provider, err)
	}

	return &models.DecryptedUserCredential{
		ID:             row.ID,
		UserID:         row.UserID,
		OrgID:          row.OrgID,
		Provider:       row.Provider,
		Config:         cfg,
		Status:         row.Status,
		LastVerifiedAt: row.LastVerifiedAt,
		UpdatedAt:      row.UpdatedAt,
	}, nil
}
