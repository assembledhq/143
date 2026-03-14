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
type UserCredentialStore struct {
	db     DBTX
	crypto *crypto.Service
}

// NewUserCredentialStore creates a new user credential store.
func NewUserCredentialStore(db DBTX, cryptoSvc *crypto.Service) *UserCredentialStore {
	return &UserCredentialStore{db: db, crypto: cryptoSvc}
}

// Upsert encrypts and stores a user credential.
func (s *UserCredentialStore) Upsert(ctx context.Context, userID, orgID uuid.UUID, cfg models.ProviderConfig, isTeamDefault bool) error {
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
		INSERT INTO user_credentials (user_id, org_id, provider, config, is_team_default, status)
		VALUES (@user_id, @org_id, @provider, @config, @is_team_default, 'active')
		ON CONFLICT (user_id, provider)
		DO UPDATE SET config = @config, is_team_default = @is_team_default, status = 'active', updated_at = now()
		RETURNING id, created_at, updated_at`

	args := pgx.NamedArgs{
		"user_id":         userID,
		"org_id":          orgID,
		"provider":        string(provider),
		"config":          encrypted,
		"is_team_default": isTeamDefault,
	}

	var id uuid.UUID
	var createdAt, updatedAt interface{}
	err = s.db.QueryRow(ctx, query, args).Scan(&id, &createdAt, &updatedAt)
	if err != nil {
		return fmt.Errorf("upsert user %s credential: %w", provider, err)
	}
	return nil
}

// GetForUser retrieves a user's credential for a specific provider.
func (s *UserCredentialStore) GetForUser(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) (*models.DecryptedUserCredential, error) {
	query := `
		SELECT id, user_id, org_id, provider, config, is_team_default, status, last_verified_at, created_at, updated_at
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

// GetTeamDefault retrieves the team default credential for a provider in an org.
func (s *UserCredentialStore) GetTeamDefault(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedUserCredential, error) {
	query := `
		SELECT id, user_id, org_id, provider, config, is_team_default, status, last_verified_at, created_at, updated_at
		FROM user_credentials
		WHERE org_id = @org_id AND provider = @provider AND is_team_default = true AND status = 'active'
		LIMIT 1`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": string(provider),
	})
	if err != nil {
		return nil, fmt.Errorf("query team default %s credential: %w", provider, err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.UserCredential])
	if err != nil {
		return nil, fmt.Errorf("get team default %s credential: %w", provider, err)
	}

	return s.decryptRow(row)
}

// ListByUser returns all credentials for a user within an org.
func (s *UserCredentialStore) ListByUser(ctx context.Context, orgID, userID uuid.UUID) ([]models.DecryptedUserCredential, error) {
	query := `
		SELECT id, user_id, org_id, provider, config, is_team_default, status, last_verified_at, created_at, updated_at
		FROM user_credentials
		WHERE org_id = @org_id AND user_id = @user_id AND status != 'disabled'
		ORDER BY provider`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":  orgID,
		"user_id": userID,
	})
	if err != nil {
		return nil, fmt.Errorf("query user credentials: %w", err)
	}
	dbRows, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.UserCredential])
	if err != nil {
		return nil, fmt.Errorf("collect user credentials: %w", err)
	}

	var creds []models.DecryptedUserCredential
	for _, row := range dbRows {
		cred, err := s.decryptRow(row)
		if err != nil {
			return nil, err
		}
		creds = append(creds, *cred)
	}
	return creds, nil
}

// ListTeamDefaults returns all team default credentials for an org.
func (s *UserCredentialStore) ListTeamDefaults(ctx context.Context, orgID uuid.UUID) ([]models.DecryptedUserCredential, error) {
	query := `
		SELECT id, user_id, org_id, provider, config, is_team_default, status, last_verified_at, created_at, updated_at
		FROM user_credentials
		WHERE org_id = @org_id AND is_team_default = true AND status = 'active'
		ORDER BY provider`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query team defaults: %w", err)
	}
	dbRows, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.UserCredential])
	if err != nil {
		return nil, fmt.Errorf("collect team defaults: %w", err)
	}

	var creds []models.DecryptedUserCredential
	for _, row := range dbRows {
		cred, err := s.decryptRow(row)
		if err != nil {
			return nil, err
		}
		creds = append(creds, *cred)
	}
	return creds, nil
}

// Disable soft-deletes a user credential.
func (s *UserCredentialStore) Disable(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) error {
	query := `UPDATE user_credentials SET status = 'disabled', updated_at = now() WHERE org_id = @org_id AND user_id = @user_id AND provider = @provider`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"user_id":  userID,
		"provider": string(provider),
	})
	return err
}

// ClearTeamDefault unsets is_team_default for a provider across all users in the org.
func (s *UserCredentialStore) ClearTeamDefault(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) error {
	query := `UPDATE user_credentials SET is_team_default = false, updated_at = now() WHERE org_id = @org_id AND provider = @provider AND is_team_default = true`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": string(provider),
	})
	return err
}

// SetTeamDefault sets a user's credential as the team default for a provider,
// clearing any existing team default for that provider in the org.
func (s *UserCredentialStore) SetTeamDefault(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) error {
	// Clear existing team default.
	if err := s.ClearTeamDefault(ctx, orgID, provider); err != nil {
		return fmt.Errorf("clear team default: %w", err)
	}

	// Set the new team default.
	query := `UPDATE user_credentials SET is_team_default = true, updated_at = now() WHERE org_id = @org_id AND user_id = @user_id AND provider = @provider AND status = 'active'`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"user_id":  userID,
		"provider": string(provider),
	})
	return err
}

// RemoveTeamDefault removes the team default for a provider in an org.
func (s *UserCredentialStore) RemoveTeamDefault(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) error {
	return s.ClearTeamDefault(ctx, orgID, provider)
}

// encrypt handles encryption or dev-mode plaintext storage.
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
		IsTeamDefault:  row.IsTeamDefault,
		Status:         row.Status,
		LastVerifiedAt: row.LastVerifiedAt,
		UpdatedAt:      row.UpdatedAt,
	}, nil
}
