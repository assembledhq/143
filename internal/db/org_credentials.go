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

// ErrCredentialLabelTaken is returned by InsertPendingAuth when the
// (org, provider, label) tuple already references a credential that cannot
// safely be overwritten (active or invalid). Callers should surface a
// status-aware message; the embedded ExistingStatus tells them which.
type ErrCredentialLabelTaken struct {
	Label          string
	ExistingStatus string
}

func (e *ErrCredentialLabelTaken) Error() string {
	switch e.ExistingStatus {
	case "active":
		return fmt.Sprintf("a credential with label %q is already connected — disconnect it first or choose a different label", e.Label)
	case "invalid":
		return fmt.Sprintf("a credential with label %q has an invalid refresh token — disconnect it first to reconnect", e.Label)
	default:
		return fmt.Sprintf("a credential with label %q already exists (status %q) — choose a different label", e.Label, e.ExistingStatus)
	}
}

// credentialColumns is the standard SELECT column list for org_credentials queries.
const credentialColumns = "id, org_id, provider, label, config, status, last_verified_at, last_used_at, created_by, created_at, updated_at"                 // #nosec G101 -- SQL column list, not credentials
const codingCredentialColumns = "id, org_id, provider, label, config, status, priority, last_verified_at, last_used_at, created_by, created_at, updated_at" // #nosec G101 -- SQL column list, not credentials

// OrgCredentialStore manages org-level API credentials for non-coding
// integrations (GitHub app keys, Sentry, Linear, Notion, Slack, Mezmo, …).
// Coding-agent credentials live exclusively in `coding_credentials`
// (CodingCredentialStore); the dual-write mirror era ended with the
// unified-coding-credentials cleanup.
type OrgCredentialStore struct {
	db     DBTX
	crypto *crypto.Service // nil = dev mode (plaintext with v0: prefix)
}

// NewOrgCredentialStore creates a new credential store.
func NewOrgCredentialStore(db DBTX, cryptoSvc *crypto.Service) *OrgCredentialStore {
	return &OrgCredentialStore{db: db, crypto: cryptoSvc}
}

// Upsert encrypts and stores a strongly-typed provider config (label defaults to "").
// created_by is not tracked for this shorthand; use UpsertWithLabel when you have a user ID.
func (s *OrgCredentialStore) Upsert(ctx context.Context, orgID uuid.UUID, cfg models.ProviderConfig) error {
	_, err := s.UpsertWithLabel(ctx, orgID, nil, "", cfg)
	return err
}

// UpsertWithLabel encrypts and stores a provider config with a specific label.
// This allows multiple credentials per org+provider (e.g. multiple ChatGPT subscriptions).
// createdBy is recorded only on INSERT — on conflict the existing created_by is preserved
// so we remember who originally added the credential.
func (s *OrgCredentialStore) UpsertWithLabel(ctx context.Context, orgID uuid.UUID, createdBy *uuid.UUID, label string, cfg models.ProviderConfig) (*uuid.UUID, error) {
	provider := cfg.Provider()

	encrypted, err := s.marshalAndEncrypt(cfg)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", provider, err)
	}

	// Coding providers live in coding_credentials and never reach this store;
	// ScopedCredentialStore routes them to CodingCredentialStore.
	// org_credentials rows ignore priority — it was only meaningful for the
	// coding-agent fallback stack — and do not stamp last_verified_at on
	// config saves (a save is not a verification).
	query := `
		INSERT INTO org_credentials (org_id, provider, label, config, status, created_by)
		VALUES (@org_id, @provider, @label, @config, 'active', @created_by)
		ON CONFLICT (org_id, provider, label)
		DO UPDATE SET config = EXCLUDED.config, status = 'active', updated_at = now()
		RETURNING id`

	args := pgx.NamedArgs{
		"org_id":     orgID,
		"provider":   string(provider),
		"label":      label,
		"config":     encrypted,
		"created_by": createdBy,
	}

	var id uuid.UUID
	err = s.db.QueryRow(ctx, query, args).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("upsert %s credential: %w", provider, err)
	}
	return &id, nil
}

// UpdateLinearConfigIfRefreshTokenMatches updates the singleton Linear
// credential only if its stored refresh token still matches the token the
// caller just redeemed. The row is locked for the read/compare/write so a
// reconnect or peer refresh cannot be overwritten by a stale refresh response.
//
// Returns the current row config with updated=false when the refresh token has
// changed. Callers should use that config for race recovery rather than
// retrying the stale token chain.
func (s *OrgCredentialStore) UpdateLinearConfigIfRefreshTokenMatches(ctx context.Context, orgID uuid.UUID, expectedRefreshToken string, cfg models.LinearConfig) (models.LinearConfig, bool, error) {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return models.LinearConfig{}, false, fmt.Errorf("org credential store db does not support transactions")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return models.LinearConfig{}, false, fmt.Errorf("begin linear credential refresh update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	query := `
		SELECT ` + credentialColumns + `
		FROM org_credentials
		WHERE org_id = @org_id
		  AND provider = @provider
		  AND label = ''
		  AND status != 'disabled'
		FOR UPDATE`
	rows, err := tx.Query(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": string(models.ProviderLinear),
	})
	if err != nil {
		return models.LinearConfig{}, false, fmt.Errorf("query linear credential for refresh update: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.OrgCredential])
	if err != nil {
		return models.LinearConfig{}, false, fmt.Errorf("load linear credential for refresh update: %w", err)
	}

	currentCred, err := s.decryptRow(row)
	if err != nil {
		return models.LinearConfig{}, false, err
	}
	current, ok := currentCred.Config.(models.LinearConfig)
	if !ok {
		return models.LinearConfig{}, false, fmt.Errorf("linear credential config is wrong type: got %T", currentCred.Config)
	}
	if current.RefreshToken != expectedRefreshToken {
		if err := tx.Commit(ctx); err != nil {
			return models.LinearConfig{}, false, fmt.Errorf("commit skipped linear credential refresh update: %w", err)
		}
		return current, false, nil
	}

	encrypted, err := s.marshalAndEncrypt(cfg)
	if err != nil {
		return models.LinearConfig{}, false, err
	}
	tag, err := tx.Exec(ctx, `UPDATE org_credentials SET config = @config, status = 'active', updated_at = now() WHERE id = @id AND org_id = @org_id`, pgx.NamedArgs{
		"id":     row.ID,
		"org_id": orgID,
		"config": encrypted,
	})
	if err != nil {
		return models.LinearConfig{}, false, fmt.Errorf("update linear credential after refresh: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return models.LinearConfig{}, false, pgx.ErrNoRows
	}
	if err := tx.Commit(ctx); err != nil {
		return models.LinearConfig{}, false, fmt.Errorf("commit linear credential refresh update: %w", err)
	}
	return cfg, true, nil
}

// UpdatePagerDutyConfigByIDIfRefreshTokenMatches persists a refreshed PagerDuty
// credential by id, but only if the stored refresh token still matches the one
// we redeemed. PagerDuty rotates the refresh token on every redemption, so a
// mismatch means a peer (another node/process) already rotated it; in that case
// we return the current row with updated=false rather than overwriting a newer
// token chain with our now-stale one. The row is locked FOR UPDATE for the
// compare-and-swap. Mirrors UpdateLinearConfigIfRefreshTokenMatches but keys on
// credential id because PagerDuty stores labeled (potentially multiple) rows.
func (s *OrgCredentialStore) UpdatePagerDutyConfigByIDIfRefreshTokenMatches(ctx context.Context, orgID, credentialID uuid.UUID, expectedRefreshToken string, cfg models.PagerDutyConfig) (models.PagerDutyConfig, bool, error) {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return models.PagerDutyConfig{}, false, fmt.Errorf("org credential store db does not support transactions")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return models.PagerDutyConfig{}, false, fmt.Errorf("begin pagerduty credential refresh update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	query := `
		SELECT ` + credentialColumns + `
		FROM org_credentials
		WHERE id = @id
		  AND org_id = @org_id
		  AND provider = @provider
		  AND status != 'disabled'
		FOR UPDATE`
	rows, err := tx.Query(ctx, query, pgx.NamedArgs{
		"id":       credentialID,
		"org_id":   orgID,
		"provider": string(models.ProviderPagerDuty),
	})
	if err != nil {
		return models.PagerDutyConfig{}, false, fmt.Errorf("query pagerduty credential for refresh update: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.OrgCredential])
	if err != nil {
		return models.PagerDutyConfig{}, false, fmt.Errorf("load pagerduty credential for refresh update: %w", err)
	}

	currentCred, err := s.decryptRow(row)
	if err != nil {
		return models.PagerDutyConfig{}, false, err
	}
	current, ok := currentCred.Config.(models.PagerDutyConfig)
	if !ok {
		return models.PagerDutyConfig{}, false, fmt.Errorf("pagerduty credential config is wrong type: got %T", currentCred.Config)
	}
	if current.RefreshToken != expectedRefreshToken {
		if err := tx.Commit(ctx); err != nil {
			return models.PagerDutyConfig{}, false, fmt.Errorf("commit skipped pagerduty credential refresh update: %w", err)
		}
		return current, false, nil
	}

	encrypted, err := s.marshalAndEncrypt(cfg)
	if err != nil {
		return models.PagerDutyConfig{}, false, err
	}
	tag, err := tx.Exec(ctx, `UPDATE org_credentials SET config = @config, status = 'active', updated_at = now() WHERE id = @id AND org_id = @org_id`, pgx.NamedArgs{
		"id":     row.ID,
		"org_id": orgID,
		"config": encrypted,
	})
	if err != nil {
		return models.PagerDutyConfig{}, false, fmt.Errorf("update pagerduty credential after refresh: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return models.PagerDutyConfig{}, false, pgx.ErrNoRows
	}
	if err := tx.Commit(ctx); err != nil {
		return models.PagerDutyConfig{}, false, fmt.Errorf("commit pagerduty credential refresh update: %w", err)
	}
	return cfg, true, nil
}

// Get decrypts and returns the unlabeled credential for an (org, provider).
// Convention: a provider that stores a single credential per org (e.g. an
// Anthropic API key) uses label=""; providers with multiple rows per org
// (e.g. Claude Code subscriptions) use non-empty labels and should be read
// via ListByProvider or GetByProviderAndLabel. Enforcing `label = ”` here
// keeps callers like resolveProviderConfig safe against the mixed case where
// an API key and several labeled subscriptions coexist under one provider.
func (s *OrgCredentialStore) Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error) {
	query := `
		SELECT ` + credentialColumns + `
		FROM org_credentials
		WHERE org_id = @org_id AND provider = @provider AND label = '' AND status != 'disabled'`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": string(provider),
	})
	if err != nil {
		return nil, fmt.Errorf("query %s credential: %w", provider, err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.OrgCredential])
	if err != nil {
		return nil, fmt.Errorf("get %s credential: %w", provider, err)
	}

	return s.decryptRow(row)
}

// GetByID decrypts and returns a credential by its ID, scoped to org.
func (s *OrgCredentialStore) GetByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID) (*models.DecryptedCredential, error) {
	query := `
		SELECT ` + credentialColumns + `
		FROM org_credentials
		WHERE id = @id AND org_id = @org_id AND status != 'disabled'`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
	})
	if err != nil {
		return nil, fmt.Errorf("query credential by id: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.OrgCredential])
	if err != nil {
		return nil, fmt.Errorf("get credential by id: %w", err)
	}

	return s.decryptRow(row)
}

// GetByProviderAndLabel returns a single credential matching org+provider+label.
func (s *OrgCredentialStore) GetByProviderAndLabel(ctx context.Context, orgID uuid.UUID, provider models.ProviderName, label string) (*models.DecryptedCredential, error) {
	query := `
		SELECT ` + credentialColumns + `
		FROM org_credentials
		WHERE org_id = @org_id AND provider = @provider AND label = @label AND status != 'disabled'`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": string(provider),
		"label":    label,
	})
	if err != nil {
		return nil, fmt.Errorf("query %s credential by label: %w", provider, err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.OrgCredential])
	if err != nil {
		return nil, fmt.Errorf("get %s credential by label: %w", provider, err)
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
		SELECT ` + credentialColumns + `
		FROM org_credentials
		WHERE org_id = @org_id AND provider = ANY(@providers) AND status = 'active'`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":    orgID,
		"providers": providerNames,
	})
	if err != nil {
		return nil, fmt.Errorf("query LLM credentials: %w", err)
	}
	dbRows, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[models.OrgCredential])
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

// GetAllIntegrations loads active singleton integration credentials for an org,
// keyed by provider. Missing providers are omitted from the returned map.
func (s *OrgCredentialStore) GetAllIntegrations(ctx context.Context, orgID uuid.UUID, providers []models.ProviderName) (map[models.ProviderName]*models.DecryptedCredential, error) {
	out := make(map[models.ProviderName]*models.DecryptedCredential, len(providers))
	if len(providers) == 0 {
		return out, nil
	}

	providerNames := make([]string, len(providers))
	for i, p := range providers {
		providerNames[i] = string(p)
	}

	query := `
		SELECT ` + credentialColumns + `
		FROM org_credentials
		WHERE org_id = @org_id AND provider = ANY(@providers) AND label = '' AND status != 'disabled'`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":    orgID,
		"providers": providerNames,
	})
	if err != nil {
		return nil, fmt.Errorf("query integration credentials: %w", err)
	}
	dbRows, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[models.OrgCredential])
	if err != nil {
		return nil, fmt.Errorf("collect integration credentials: %w", err)
	}

	for _, row := range dbRows {
		cred, err := s.decryptRow(row)
		if err != nil {
			return nil, err
		}
		out[cred.Provider] = cred
	}
	return out, nil
}

// ListSummaries returns masked credential info for all providers.
// Returns a CredentialSummary for every known provider (configured or not).
func (s *OrgCredentialStore) ListSummaries(ctx context.Context, orgID uuid.UUID) ([]models.CredentialSummary, error) {
	query := `
		SELECT ` + credentialColumns + `
		FROM org_credentials
		WHERE org_id = @org_id AND label = '' AND status != 'disabled'`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query credentials: %w", err)
	}
	dbRows, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[models.OrgCredential])
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

// ListByProvider returns all active credentials for a given org+provider.
func (s *OrgCredentialStore) ListByProvider(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) ([]models.DecryptedCredential, error) {
	query := `
		SELECT ` + codingCredentialColumns + `
		FROM org_credentials
		WHERE org_id = @org_id AND provider = @provider AND status != 'disabled'
		ORDER BY priority, created_at`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": string(provider),
	})
	if err != nil {
		return nil, fmt.Errorf("query %s credentials: %w", provider, err)
	}
	dbRows, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[models.OrgCredential])
	if err != nil {
		return nil, fmt.Errorf("collect %s credentials: %w", provider, err)
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

// Disable soft-deletes a credential.
func (s *OrgCredentialStore) Disable(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) error {
	query := `UPDATE org_credentials SET status = 'disabled', updated_at = now() WHERE org_id = @org_id AND provider = @provider AND label = ''`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": string(provider),
	})
	return err
}

// HasActiveLabeled reports whether (org, provider) has at least one active
// credential with a non-empty label. Used by callers (e.g. the Claude Code
// subscription path) that need a cheap existence check without claiming a
// round-robin slot — claiming would bump last_used_at and distort rotation.
// Runs as a LIMIT 1 EXISTS-style probe so it stays O(1) even when an org
// has many labeled credentials.
func (s *OrgCredentialStore) HasActiveLabeled(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (bool, error) {
	var exists bool
	row := s.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM org_credentials
			WHERE org_id = @org_id AND provider = @provider AND label != '' AND status = 'active'
		)`, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": string(provider),
	})
	if err := row.Scan(&exists); err != nil {
		return false, fmt.Errorf("check active labeled %s credential: %w", provider, err)
	}
	return exists, nil
}

// DisableLabeled soft-deletes only the labeled rows for (org, provider),
// leaving the singleton label=” row untouched. Used when a provider mixes
// an API-key row (label=”) with subscription rows (label!=”) and the
// caller wants to clear only the subscriptions.
func (s *OrgCredentialStore) DisableLabeled(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) error {
	query := `UPDATE org_credentials SET status = 'disabled', updated_at = now() WHERE org_id = @org_id AND provider = @provider AND label != ''`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": string(provider),
	})
	return err
}

// DisableByID soft-deletes a specific credential by its ID, scoped to org.
func (s *OrgCredentialStore) DisableByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID) error {
	query := `UPDATE org_credentials SET status = 'disabled', updated_at = now() WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
	})
	return err
}

// ExistsForProviderByID reports whether a credential with the given id belongs
// to the org AND matches the expected provider. Includes disabled rows, so
// callers that need to tell "not mine" apart from "already disconnected" get a
// true answer in both cases. The provider filter keeps provider-specific
// endpoints (e.g. codex-auth) from affecting unrelated credentials that happen
// to share the org.
func (s *OrgCredentialStore) ExistsForProviderByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID, provider models.ProviderName) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM org_credentials WHERE id = @id AND org_id = @org_id AND provider = @provider)`,
		pgx.NamedArgs{"id": id, "org_id": orgID, "provider": string(provider)},
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check credential ownership: %w", err)
	}
	return exists, nil
}

// UpdateStatus updates the status and last_verified_at timestamp.
func (s *OrgCredentialStore) UpdateStatus(ctx context.Context, orgID uuid.UUID, provider models.ProviderName, status models.CredentialStatus) error {
	query := `UPDATE org_credentials SET status = @status, last_verified_at = now(), updated_at = now() WHERE org_id = @org_id AND provider = @provider`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": string(provider),
		"status":   string(status),
	})
	return err
}

// UpdateStatusByID updates the status for a specific credential by ID, scoped to org.
func (s *OrgCredentialStore) UpdateStatusByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID, status models.CredentialStatus) error {
	query := `UPDATE org_credentials SET status = @status, last_verified_at = now(), updated_at = now() WHERE id = @id AND org_id = @org_id`
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
		"status": string(status),
	})
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// marshalAndEncrypt serializes and encrypts a provider config for storage.
func (s *OrgCredentialStore) marshalAndEncrypt(cfg models.ProviderConfig) ([]byte, error) {
	plaintext, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	encrypted, err := s.encrypt(plaintext)
	if err != nil {
		return nil, fmt.Errorf("encrypt config: %w", err)
	}
	return encrypted, nil
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
		Label:          row.Label,
		Config:         cfg,
		Status:         row.Status,
		Priority:       row.Priority,
		LastVerifiedAt: row.LastVerifiedAt,
		LastUsedAt:     row.LastUsedAt,
		CreatedBy:      row.CreatedBy,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}, nil
}
