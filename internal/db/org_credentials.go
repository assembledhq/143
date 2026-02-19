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

// OrgCredentialStore handles encrypted per-org credential storage.
type OrgCredentialStore struct {
	db     DBTX
	crypto *crypto.Service // nil = dev mode (plaintext with v0: prefix)
}

// NewOrgCredentialStore creates a new credential store.
func NewOrgCredentialStore(db DBTX, cryptoSvc *crypto.Service) *OrgCredentialStore {
	return &OrgCredentialStore{db: db, crypto: cryptoSvc}
}

// Upsert encrypts and stores a strongly-typed provider config.
func (s *OrgCredentialStore) Upsert(ctx context.Context, orgID uuid.UUID, cfg models.ProviderConfig) error {
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
		INSERT INTO org_credentials (org_id, provider, config, status)
		VALUES (@org_id, @provider, @config, 'active')
		ON CONFLICT (org_id, provider)
		DO UPDATE SET config = @config, status = 'active', updated_at = now()
		RETURNING id, created_at, updated_at`

	args := pgx.NamedArgs{
		"org_id":   orgID,
		"provider": string(provider),
		"config":   encrypted,
	}

	var id uuid.UUID
	var createdAt, updatedAt interface{}
	err = s.db.QueryRow(ctx, query, args).Scan(&id, &createdAt, &updatedAt)
	if err != nil {
		return fmt.Errorf("upsert %s credential: %w", provider, err)
	}
	return nil
}

// Get decrypts and returns a strongly-typed credential.
func (s *OrgCredentialStore) Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error) {
	query := `
		SELECT id, org_id, provider, config, status, last_verified_at, created_at, updated_at
		FROM org_credentials
		WHERE org_id = @org_id AND provider = @provider AND status != 'disabled'`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": string(provider),
	})
	if err != nil {
		return nil, fmt.Errorf("query %s credential: %w", provider, err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.OrgCredential])
	if err != nil {
		return nil, fmt.Errorf("get %s credential: %w", provider, err)
	}

	return s.decryptRow(row)
}

// GetAllLLM loads all active LLM provider credentials for an org.
func (s *OrgCredentialStore) GetAllLLM(ctx context.Context, orgID uuid.UUID) ([]models.DecryptedCredential, error) {
	providerNames := make([]string, len(models.LLMProviders))
	for i, p := range models.LLMProviders {
		providerNames[i] = string(p)
	}

	query := `
		SELECT id, org_id, provider, config, status, last_verified_at, created_at, updated_at
		FROM org_credentials
		WHERE org_id = @org_id AND provider = ANY(@providers) AND status = 'active'`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":    orgID,
		"providers": providerNames,
	})
	if err != nil {
		return nil, fmt.Errorf("query LLM credentials: %w", err)
	}
	dbRows, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.OrgCredential])
	if err != nil {
		return nil, fmt.Errorf("collect LLM credentials: %w", err)
	}

	var creds []models.DecryptedCredential
	for _, row := range dbRows {
		cred, err := s.decryptRow(row)
		if err != nil {
			return nil, err
		}
		creds = append(creds, *cred)
	}
	return creds, nil
}

// ListSummaries returns masked credential info for all providers.
// Returns a CredentialSummary for every known provider (configured or not).
func (s *OrgCredentialStore) ListSummaries(ctx context.Context, orgID uuid.UUID) ([]models.CredentialSummary, error) {
	query := `
		SELECT id, org_id, provider, config, status, last_verified_at, created_at, updated_at
		FROM org_credentials
		WHERE org_id = @org_id AND status != 'disabled'`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query credentials: %w", err)
	}
	dbRows, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.OrgCredential])
	if err != nil {
		return nil, fmt.Errorf("collect credentials: %w", err)
	}

	// Build a map of configured providers.
	configured := make(map[models.ProviderName]models.CredentialSummary)
	for _, row := range dbRows {
		cred, err := s.decryptRow(row)
		if err != nil {
			continue // skip rows we can't decrypt
		}
		summary := cred.Config.MaskedSummary()
		summary.Status = cred.Status
		summary.LastVerifiedAt = cred.LastVerifiedAt
		configured[cred.Provider] = summary
	}

	// Return a summary for every known provider.
	var summaries []models.CredentialSummary
	for _, p := range models.AllProviders {
		if s, ok := configured[p]; ok {
			summaries = append(summaries, s)
		} else {
			summaries = append(summaries, models.CredentialSummary{
				Provider:   p,
				Configured: false,
			})
		}
	}
	return summaries, nil
}

// Disable soft-deletes a credential.
func (s *OrgCredentialStore) Disable(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) error {
	query := `UPDATE org_credentials SET status = 'disabled', updated_at = now() WHERE org_id = @org_id AND provider = @provider`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": string(provider),
	})
	return err
}

// UpdateStatus updates the status and last_verified_at timestamp.
func (s *OrgCredentialStore) UpdateStatus(ctx context.Context, orgID uuid.UUID, provider models.ProviderName, status string) error {
	query := `UPDATE org_credentials SET status = @status, last_verified_at = now(), updated_at = now() WHERE org_id = @org_id AND provider = @provider`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": string(provider),
		"status":   status,
	})
	return err
}

// encrypt handles encryption or dev-mode plaintext storage.
func (s *OrgCredentialStore) encrypt(plaintext []byte) ([]byte, error) {
	if s.crypto != nil {
		return s.crypto.Encrypt(plaintext)
	}
	return crypto.DevEncrypt(plaintext), nil
}

// decrypt handles decryption or dev-mode plaintext reading.
func (s *OrgCredentialStore) decrypt(data []byte) ([]byte, error) {
	if s.crypto != nil {
		return s.crypto.Decrypt(data)
	}
	return crypto.DevDecrypt(data)
}

// decryptRow decrypts a DB row and parses into a DecryptedCredential.
func (s *OrgCredentialStore) decryptRow(row models.OrgCredential) (*models.DecryptedCredential, error) {
	plaintext, err := s.decrypt(row.Config)
	if err != nil {
		return nil, fmt.Errorf("decrypt %s config: %w", row.Provider, err)
	}

	cfg, err := models.ParseProviderConfig(row.Provider, plaintext)
	if err != nil {
		return nil, fmt.Errorf("parse %s config: %w", row.Provider, err)
	}

	return &models.DecryptedCredential{
		ID:             row.ID,
		OrgID:          row.OrgID,
		Provider:       row.Provider,
		Config:         cfg,
		Status:         row.Status,
		LastVerifiedAt: row.LastVerifiedAt,
		UpdatedAt:      row.UpdatedAt,
	}, nil
}
