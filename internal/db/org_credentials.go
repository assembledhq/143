package db

import (
	"context"
	"encoding/json"
	"errors"
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
const credentialColumns = "id, org_id, provider, label, config, status, last_verified_at, last_used_at, created_by, created_at, updated_at" // #nosec G101 -- SQL column list, not credentials

// OrgCredentialStore manages org-level API credentials (e.g. Anthropic API keys, OpenAI keys).
// These are distinct from integrations (which store third-party platform connections like GitHub,
// Sentry, Linear). The credential store holds keys used for AI model access and other
// infrastructure services, while integrations hold OAuth tokens and webhook configs for
// external platform connectivity.
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

	query := `
		INSERT INTO org_credentials (org_id, provider, label, config, status, created_by)
		VALUES (@org_id, @provider, @label, @config, 'active', @created_by)
		ON CONFLICT (org_id, provider, label)
		DO UPDATE SET config = @config, status = 'active', updated_at = now()
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

// InsertPendingAuth inserts a new pending-auth credential row.
// Unlike UpsertWithLabel, this does NOT overwrite an existing credential that
// holds a real access token. Disabled rows are allowed to be resurrected so
// that a user who disconnected a label can re-add the same label without
// having to pick a new one.
//
// On a conflict where the existing row is active or invalid, returns a typed
// *ErrCredentialLabelTaken so callers can render a status-appropriate message.
// createdBy is recorded only on INSERT; a conflicting row keeps its original created_by.
func (s *OrgCredentialStore) InsertPendingAuth(ctx context.Context, orgID uuid.UUID, createdBy *uuid.UUID, label string, cfg models.ProviderConfig) (*uuid.UUID, error) {
	provider := cfg.Provider()

	encrypted, err := s.marshalAndEncrypt(cfg)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", provider, err)
	}

	// ON CONFLICT only updates if the existing row is pending_auth or disabled —
	// never stomps on a credential that holds a real access token (active) or one
	// that's still mid-rotation in the user's mental model (invalid).
	query := `
		INSERT INTO org_credentials (org_id, provider, label, config, status, created_by)
		VALUES (@org_id, @provider, @label, @config, 'pending_auth', @created_by)
		ON CONFLICT (org_id, provider, label)
		DO UPDATE SET config = @config, status = 'pending_auth', updated_at = now()
		WHERE org_credentials.status IN ('pending_auth', 'disabled')
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
		if errors.Is(err, pgx.ErrNoRows) {
			// Conflict on (org, provider, label) but the existing row is
			// active/invalid. Look up the actual status to surface a useful error.
			existingStatus, lookupErr := s.lookupCredentialStatus(ctx, orgID, provider, label)
			if lookupErr != nil {
				return nil, &ErrCredentialLabelTaken{Label: label, ExistingStatus: "unknown"}
			}
			return nil, &ErrCredentialLabelTaken{Label: label, ExistingStatus: existingStatus}
		}
		return nil, fmt.Errorf("insert pending %s credential: %w", provider, err)
	}
	return &id, nil
}

// lookupCredentialStatus returns the raw status string for a (org, provider, label)
// row. Used by InsertPendingAuth to disambiguate conflict cases.
func (s *OrgCredentialStore) lookupCredentialStatus(ctx context.Context, orgID uuid.UUID, provider models.ProviderName, label string) (string, error) {
	var status string
	err := s.db.QueryRow(ctx,
		`SELECT status FROM org_credentials WHERE org_id = @org_id AND provider = @provider AND label = @label`,
		pgx.NamedArgs{"org_id": orgID, "provider": string(provider), "label": label},
	).Scan(&status)
	return status, err
}

// UpsertByID updates an existing credential's config by ID, scoped to org.
// Refuses to resurrect a disabled row: if a user disconnects a credential
// while a refresh is mid-flight, this prevents the refresh from silently
// flipping the row back to active.
func (s *OrgCredentialStore) UpsertByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID, cfg models.ProviderConfig) error {
	encrypted, err := s.marshalAndEncrypt(cfg)
	if err != nil {
		return err
	}

	query := `UPDATE org_credentials SET config = @config, status = 'active', updated_at = now() WHERE id = @id AND org_id = @org_id AND status != 'disabled'`
	_, err = s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
		"config": encrypted,
	})
	return err
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
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.OrgCredential])
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
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.OrgCredential])
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
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.OrgCredential])
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
		SELECT ` + credentialColumns + `
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

// ListByProvider returns all active credentials for a given org+provider.
func (s *OrgCredentialStore) ListByProvider(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) ([]models.DecryptedCredential, error) {
	query := `
		SELECT ` + credentialColumns + `
		FROM org_credentials
		WHERE org_id = @org_id AND provider = @provider AND status != 'disabled'
		ORDER BY created_at`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": string(provider),
	})
	if err != nil {
		return nil, fmt.Errorf("query %s credentials: %w", provider, err)
	}
	dbRows, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.OrgCredential])
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

// ClaimNextLabeledRoundRobin is the subscription-scoped variant of
// ClaimNextRoundRobin: it claims the LRU active credential whose label is
// non-empty. This is how providers that mix a singleton API-key row
// (label = ”) with multiple labeled subscription rows (e.g. ProviderAnthropic
// holding both an Anthropic API key and Claude Code subscriptions) keep
// round-robin scoped to the subscription set. Locking semantics and the
// "preemptive last_used_at" tradeoff match ClaimNextRoundRobin.
func (s *OrgCredentialStore) ClaimNextLabeledRoundRobin(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error) {
	query := `
		WITH next AS (
			SELECT id FROM org_credentials
			WHERE org_id = @org_id AND provider = @provider AND label != '' AND status = 'active'
			ORDER BY last_used_at NULLS FIRST, created_at
			LIMIT 1
			FOR UPDATE
		)
		UPDATE org_credentials c
		SET last_used_at = now(), updated_at = now()
		FROM next
		WHERE c.id = next.id
		RETURNING c.id, c.org_id, c.provider, c.label, c.config, c.status, c.last_verified_at, c.last_used_at, c.created_by, c.created_at, c.updated_at`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": string(provider),
	})
	if err != nil {
		return nil, fmt.Errorf("query next labeled round-robin %s credential: %w", provider, err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.OrgCredential])
	if err != nil {
		return nil, fmt.Errorf("get next labeled round-robin %s credential: %w", provider, err)
	}

	return s.decryptRow(row)
}

// ClaimNextRoundRobin atomically selects the active credential with the oldest
// last_used_at and marks it as used. The row-level FOR UPDATE lock serializes
// concurrent claims so each request consistently sees the latest last_used_at,
// preventing two callers from picking the same credential.
//
// We deliberately do NOT use SKIP LOCKED: if all candidate rows are briefly
// locked by concurrent claims, SKIP LOCKED would return zero rows even though
// a valid credential exists. Waiting for the lock is correct here because
// claims are fast (one UPDATE) and a single-credential org would otherwise
// fail spuriously under load.
//
// last_used_at is bumped preemptively — before the caller knows whether the
// downstream request actually succeeded. That's a deliberate trade-off: a
// failed request still "consumes" the credential's turn in the rotation, but
// the alternative (update on success) would require a second round-trip and
// reintroduce the double-claim race that FOR UPDATE is here to prevent.
func (s *OrgCredentialStore) ClaimNextRoundRobin(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error) {
	query := `
		WITH next AS (
			SELECT id FROM org_credentials
			WHERE org_id = @org_id AND provider = @provider AND status = 'active'
			ORDER BY last_used_at NULLS FIRST, created_at
			LIMIT 1
			FOR UPDATE
		)
		UPDATE org_credentials c
		SET last_used_at = now(), updated_at = now()
		FROM next
		WHERE c.id = next.id
		RETURNING c.id, c.org_id, c.provider, c.label, c.config, c.status, c.last_verified_at, c.last_used_at, c.created_by, c.created_at, c.updated_at`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": string(provider),
	})
	if err != nil {
		return nil, fmt.Errorf("query next round-robin %s credential: %w", provider, err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.OrgCredential])
	if err != nil {
		return nil, fmt.Errorf("get next round-robin %s credential: %w", provider, err)
	}

	return s.decryptRow(row)
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
func (s *OrgCredentialStore) UpdateStatus(ctx context.Context, orgID uuid.UUID, provider models.ProviderName, status string) error {
	query := `UPDATE org_credentials SET status = @status, last_verified_at = now(), updated_at = now() WHERE org_id = @org_id AND provider = @provider`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": string(provider),
		"status":   status,
	})
	return err
}

// UpdateStatusByID updates the status for a specific credential by ID, scoped to org.
func (s *OrgCredentialStore) UpdateStatusByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID, status string) error {
	query := `UPDATE org_credentials SET status = @status, last_verified_at = now(), updated_at = now() WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
		"status": status,
	})
	return err
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
		LastVerifiedAt: row.LastVerifiedAt,
		LastUsedAt:     row.LastUsedAt,
		CreatedBy:      row.CreatedBy,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}, nil
}
