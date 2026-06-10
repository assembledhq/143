package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/crypto"
	"github.com/assembledhq/143/internal/models"
)

// codingAuthProviders is the canonical list of providers that participate in
// the org-level coding-auth fallback stack. Priority is meaningful only for
// these — other credentials (GitHub, Sentry, …) keep the column default.
//
// This is intentionally distinct from models.CodingAgentProviders, which
// enumerates personal-credential coding providers: openai_chatgpt is the
// Codex subscription/OAuth flavor (org-only), and openrouter is currently
// only wired into personal LLM credentials, not the org fallback stack.
var codingAuthProviders = []models.ProviderName{
	models.ProviderAnthropic,
	models.ProviderOpenAI,
	models.ProviderOpenAIChatGPT,
	models.ProviderGemini,
	models.ProviderAmp,
	models.ProviderPi,
}

// codingAuthProviderSQLList renders codingAuthProviders as a parenthesized
// SQL value list (e.g. "('anthropic', 'openai', …)") for embedding directly
// into `provider IN ...` clauses. Provider values are typed constants known
// at compile time, so there is no SQL injection surface.
var codingAuthProviderSQLList = func() string {
	parts := make([]string, len(codingAuthProviders))
	for i, p := range codingAuthProviders {
		parts[i] = "'" + string(p) + "'"
	}
	return "(" + strings.Join(parts, ", ") + ")"
}()

func isCodingAuthProvider(provider models.ProviderName) bool {
	for _, p := range codingAuthProviders {
		if p == provider {
			return true
		}
	}
	return false
}

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

// OrgCredentialStore manages org-level API credentials (e.g. Anthropic API keys, OpenAI keys).
// These are distinct from integrations (which store third-party platform connections like GitHub,
// Sentry, Linear). The credential store holds keys used for AI model access and other
// infrastructure services, while integrations hold OAuth tokens and webhook configs for
// external platform connectivity.
//
// During the unified-coding-credentials migration window, every coding-provider
// write here is also mirrored into the unified `coding_credentials` table via
// the codingMirror. main.go calls SetCodingMirror after construction to wire it.
// The mirror is removed in the cleanup PR.
type OrgCredentialStore struct {
	db           DBTX
	crypto       *crypto.Service // nil = dev mode (plaintext with v0: prefix)
	codingMirror CodingCredentialMirror
	mirrorLogf   func(format string, args ...any) // optional structured-log hook for mirror failures
}

// NewOrgCredentialStore creates a new credential store.
func NewOrgCredentialStore(db DBTX, cryptoSvc *crypto.Service) *OrgCredentialStore {
	return &OrgCredentialStore{db: db, crypto: cryptoSvc, codingMirror: NoopMirror()}
}

// SetCodingMirror installs the unified-credentials mirror. Calling with nil
// reverts to a no-op mirror. Must be called before serving traffic — the
// mirror itself is goroutine-safe but the field is not under a mutex.
//
// lint:allow-no-orgid reason="process-wide mirror configuration; not tenant data"
func (s *OrgCredentialStore) SetCodingMirror(m CodingCredentialMirror) {
	if m == nil {
		s.codingMirror = NoopMirror()
		return
	}
	s.codingMirror = m
}

// SetMirrorLogger installs a structured-log hook used when the mirror write
// fails. Production wires the application logger; tests usually leave it nil.
//
// lint:allow-no-orgid reason="process-wide logger configuration; not tenant data"
func (s *OrgCredentialStore) SetMirrorLogger(logf func(format string, args ...any)) {
	s.mirrorLogf = logf
}

func (s *OrgCredentialStore) logMirrorFailure(action string, id uuid.UUID, err error) {
	if s.mirrorLogf == nil || err == nil {
		return
	}
	s.mirrorLogf("coding_credentials mirror %s id=%s err=%v", action, id, err)
}

// reflectOrgCredentialByID loads the row for `id` and asks the versioned
// coding-credentials mirror to reflect it. Used after every legacy write whose
// return shape doesn't already give us the full row.
func (s *OrgCredentialStore) reflectOrgCredentialByID(ctx context.Context, orgID, id uuid.UUID) error {
	if s.codingMirror == nil || isNoopCodingCredentialMirror(s.codingMirror) {
		return nil
	}
	row, cfg, err := s.loadOrgCredentialByID(ctx, orgID, id)
	if err != nil {
		s.logMirrorFailure("load-by-id", id, err)
		return fmt.Errorf("load org credential for versioned mirror: %w", err)
	}
	if mirrErr := s.codingMirror.MirrorOrgCredential(ctx, row, cfg); mirrErr != nil {
		s.logMirrorFailure("upsert", id, mirrErr)
		return fmt.Errorf("mirror org credential into versioned coding_credentials: %w", mirrErr)
	}
	return nil
}

// loadOrgCredentialByID is a private helper that reads a row + decrypts it
// without going through the public Get path (which filters out disabled rows
// and would prevent the mirror from reflecting state-flip writes).
func (s *OrgCredentialStore) loadOrgCredentialByID(ctx context.Context, orgID, id uuid.UUID) (models.OrgCredential, models.ProviderConfig, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+codingCredentialColumns+` FROM org_credentials WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": id, "org_id": orgID},
	)
	if err != nil {
		return models.OrgCredential{}, nil, fmt.Errorf("load org credential: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.OrgCredential])
	if err != nil {
		return models.OrgCredential{}, nil, fmt.Errorf("load org credential row: %w", err)
	}
	plaintext, err := s.decrypt(row.Config)
	if err != nil {
		return row, nil, fmt.Errorf("decrypt for mirror: %w", err)
	}
	cfg, err := jsonDecodeProvider(row.Provider, plaintext)
	if err != nil {
		return row, nil, err
	}
	return row, cfg, nil
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

	if isCodingAuthProvider(provider) {
		var id uuid.UUID
		err := s.withCodingAuthPriority(ctx, orgID, func(tx pgx.Tx, nextPriority int) error {
			query := `
				INSERT INTO org_credentials (org_id, provider, label, config, status, created_by, priority)
				VALUES (@org_id, @provider, @label, @config, 'active', @created_by, @priority)
				ON CONFLICT (org_id, provider, label)
				DO UPDATE SET config = EXCLUDED.config, status = 'active', last_verified_at = now(), updated_at = now(),
					priority = CASE
						WHEN org_credentials.status = 'disabled' THEN EXCLUDED.priority
						ELSE org_credentials.priority
					END
				RETURNING id`

			args := pgx.NamedArgs{
				"org_id":     orgID,
				"provider":   string(provider),
				"label":      label,
				"config":     encrypted,
				"created_by": createdBy,
				"priority":   nextPriority,
			}

			if err := tx.QueryRow(ctx, query, args).Scan(&id); err != nil {
				return fmt.Errorf("upsert %s credential: %w", provider, err)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if err := s.reflectOrgCredentialByID(ctx, orgID, id); err != nil {
			return nil, err
		}
		return &id, nil
	}

	// Non-coding providers (GitHub, Sentry, Linear, Notion, …) ignore priority
	// — it's only meaningful for the coding-agent fallback stack. They also do
	// not stamp last_verified_at: only the coding path above treats an upsert
	// as a verified-credential replacement (the OAuth services hand it material
	// the upstream provider just accepted, and the mirror propagates the
	// timestamp into the versioned runtime state).
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
	// We're already in the non-coding branch (the coding-provider path returned
	// above), so skip the mirror reflect call entirely. Calling it here would
	// run a SELECT + decrypt for github_app/sentry/linear/notion/slack writes
	// only to discover that mirrorProviderForOrg returns ok=false and emits
	// nothing — wasted work on every integration write.
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
	// that's still mid-rotation in the user's mental model (invalid). When
	// resurrecting a disabled row we bump its priority to the next slot so it
	// appears at the bottom of the fallback stack rather than carrying over an
	// old position the user may not remember.
	var id uuid.UUID
	err = s.withCodingAuthPriority(ctx, orgID, func(tx pgx.Tx, nextPriority int) error {
		query := `
			INSERT INTO org_credentials (org_id, provider, label, config, status, created_by, priority)
			VALUES (@org_id, @provider, @label, @config, 'pending_auth', @created_by, @priority)
			ON CONFLICT (org_id, provider, label)
			DO UPDATE SET config = EXCLUDED.config, status = 'pending_auth', updated_at = now(),
				priority = CASE
					WHEN org_credentials.status = 'disabled' THEN EXCLUDED.priority
					ELSE org_credentials.priority
				END
			WHERE org_credentials.status IN ('pending_auth', 'disabled')
			RETURNING id`

		args := pgx.NamedArgs{
			"org_id":     orgID,
			"provider":   string(provider),
			"label":      label,
			"config":     encrypted,
			"created_by": createdBy,
			"priority":   nextPriority,
		}

		scanErr := tx.QueryRow(ctx, query, args).Scan(&id)
		if scanErr == nil {
			return nil
		}
		if !errors.Is(scanErr, pgx.ErrNoRows) {
			return fmt.Errorf("insert pending %s credential: %w", provider, scanErr)
		}
		// Conflict on (org, provider, label) but the existing row is
		// active/invalid. Look up the actual status (still inside the
		// transaction so the read is consistent with the failed insert)
		// to surface a useful error to the caller.
		existingStatus := "unknown"
		var status string
		if lookupErr := tx.QueryRow(ctx,
			`SELECT status FROM org_credentials WHERE org_id = @org_id AND provider = @provider AND label = @label`,
			pgx.NamedArgs{"org_id": orgID, "provider": string(provider), "label": label},
		).Scan(&status); lookupErr == nil {
			existingStatus = status
		}
		return &ErrCredentialLabelTaken{Label: label, ExistingStatus: existingStatus}
	})
	if err != nil {
		return nil, err
	}
	if err := s.reflectOrgCredentialByID(ctx, orgID, id); err != nil {
		return nil, err
	}
	return &id, nil
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

	query := `UPDATE org_credentials SET config = @config, status = 'active', last_verified_at = now(), updated_at = now() WHERE id = @id AND org_id = @org_id AND status != 'disabled'`
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
		"config": encrypted,
	})
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if mirrorErr := s.reflectOrgCredentialByID(ctx, orgID, id); mirrorErr != nil {
		return mirrorErr
	}
	return nil
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

// ClaimNextLabeledRoundRobin is the subscription-scoped variant of
// ClaimNextRoundRobin: it claims the highest-priority active credential whose
// label is non-empty, using last_used_at only as a tie-breaker within a
// priority tier. This is how providers that mix a singleton API-key row
// (label = ”) with multiple labeled subscription rows (e.g. ProviderAnthropic
// holding both an Anthropic API key and Claude Code subscriptions) keep
// selection scoped to the subscription set. Locking semantics and the
// "preemptive last_used_at" tradeoff match ClaimNextRoundRobin.
func (s *OrgCredentialStore) ClaimNextLabeledRoundRobin(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error) {
	query := `
		WITH next AS (
			SELECT id FROM org_credentials
			WHERE org_id = @org_id AND provider = @provider AND label != '' AND status = 'active'
			ORDER BY priority, last_used_at NULLS FIRST, created_at
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
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.OrgCredential])
	if err != nil {
		return nil, fmt.Errorf("get next labeled round-robin %s credential: %w", provider, err)
	}

	return s.decryptRow(row)
}

// ClaimNextRoundRobin atomically selects the highest-priority active
// credential, using last_used_at as a tie-breaker within a priority tier, and
// marks it as used. The row-level FOR UPDATE lock serializes concurrent claims
// so each request consistently sees the latest last_used_at, preventing two
// callers from picking the same credential.
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
			ORDER BY priority, last_used_at NULLS FIRST, created_at
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
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.OrgCredential])
	if err != nil {
		return nil, fmt.Errorf("get next round-robin %s credential: %w", provider, err)
	}

	return s.decryptRow(row)
}

// Disable soft-deletes a credential.
func (s *OrgCredentialStore) Disable(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) error {
	// Capture affected ids for the mirror before we issue the UPDATE. The pre-SELECT
	// is consistent: a concurrent insert at this label will not appear in our
	// snapshot, but any newly-inserted row is already mirrored by its own write
	// path, so the mirror does not need to know about it here.
	mirrorIDs := s.findOrgCredentialIDsForMirror(ctx, orgID, provider, false)
	query := `UPDATE org_credentials SET status = 'disabled', updated_at = now() WHERE org_id = @org_id AND provider = @provider AND label = ''`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": string(provider),
	})
	if err != nil {
		return err
	}
	for _, id := range mirrorIDs {
		if mirrErr := s.codingMirror.MirrorOrgCredentialDisable(ctx, orgID, id); mirrErr != nil {
			s.logMirrorFailure("disable", id, mirrErr)
		}
	}
	return nil
}

// findOrgCredentialIDsForMirror returns the ids that match the given (provider,
// label-shape) so the caller can mirror a bulk operation. labelNotEmpty=false
// means "label = ”"; labelNotEmpty=true means "label != ”". Errors are
// swallowed — the mirror is best-effort.
func (s *OrgCredentialStore) findOrgCredentialIDsForMirror(ctx context.Context, orgID uuid.UUID, provider models.ProviderName, labelNotEmpty bool) []uuid.UUID {
	clause := "label = ''"
	if labelNotEmpty {
		clause = "label != ''"
	}
	rows, err := s.db.Query(ctx,
		`SELECT id FROM org_credentials WHERE org_id = @org_id AND provider = @provider AND `+clause,
		pgx.NamedArgs{"org_id": orgID, "provider": string(provider)},
	)
	if err != nil {
		return nil
	}
	return collectMirrorIDs(rows)
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
	mirrorIDs := s.findOrgCredentialIDsForMirror(ctx, orgID, provider, true)
	query := `UPDATE org_credentials SET status = 'disabled', updated_at = now() WHERE org_id = @org_id AND provider = @provider AND label != ''`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": string(provider),
	})
	if err != nil {
		return err
	}
	for _, id := range mirrorIDs {
		if mirrErr := s.codingMirror.MirrorOrgCredentialDisable(ctx, orgID, id); mirrErr != nil {
			s.logMirrorFailure("disable-labeled", id, mirrErr)
		}
	}
	return nil
}

// DisableByID soft-deletes a specific credential by its ID, scoped to org.
func (s *OrgCredentialStore) DisableByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID) error {
	query := `UPDATE org_credentials SET status = 'disabled', updated_at = now() WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
	})
	if err == nil {
		if mirrErr := s.codingMirror.MirrorOrgCredentialDisable(ctx, orgID, id); mirrErr != nil {
			s.logMirrorFailure("disable-by-id", id, mirrErr)
		}
	}
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
	// Snapshot the affected ids so the post-update mirror can flip status on
	// each one. Pre-SELECT keeps the public Exec call path identical, which
	// keeps the mock-based unit tests green.
	mirrorRows, qErr := s.db.Query(ctx,
		`SELECT id FROM org_credentials WHERE org_id = @org_id AND provider = @provider`,
		pgx.NamedArgs{"org_id": orgID, "provider": string(provider)},
	)
	var mirrorIDs []uuid.UUID
	if qErr == nil {
		mirrorIDs = collectMirrorIDs(mirrorRows)
	}
	query := `UPDATE org_credentials SET status = @status, last_verified_at = now(), updated_at = now() WHERE org_id = @org_id AND provider = @provider`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": string(provider),
		"status":   string(status),
	})
	if err != nil {
		return err
	}
	for _, id := range mirrorIDs {
		if err := s.reflectOrgCredentialByID(ctx, orgID, id); err != nil {
			return err
		}
	}
	return nil
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
	if mirrorErr := s.reflectOrgCredentialByID(ctx, orgID, id); mirrorErr != nil {
		return mirrorErr
	}
	return nil
}

// collectMirrorIDs walks a pgx.Rows result of a single uuid column,
// swallowing scan errors per row. Used by the mirror-aware write paths;
// distinct from the package's strict collectIDs (which fails the whole call
// on the first scan error). Mirror writes are best-effort — losing one id
// to a scan glitch costs only that mirror; the legacy write already succeeded.
func collectMirrorIDs(rows pgx.Rows) []uuid.UUID {
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			continue
		}
		out = append(out, id)
	}
	return out
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

func (s *OrgCredentialStore) ListCodingAuths(ctx context.Context, orgID uuid.UUID) ([]models.CodingAuth, error) {
	query := `
		SELECT ` + codingCredentialColumns + `
		FROM org_credentials
		WHERE org_id = @org_id
		  AND status != 'disabled'
		  AND provider IN ` + codingAuthProviderSQLList + `
		ORDER BY priority, created_at`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return nil, fmt.Errorf("query coding auths: %w", err)
	}
	dbRows, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[models.OrgCredential])
	if err != nil {
		return nil, fmt.Errorf("collect coding auths: %w", err)
	}

	decrypted := make([]models.DecryptedCredential, 0, len(dbRows))
	for _, row := range dbRows {
		cred, derr := s.decryptRow(row)
		if derr != nil {
			return nil, derr
		}
		if !isSupportedCodingAuthCredential(*cred) {
			continue
		}
		decrypted = append(decrypted, *cred)
	}

	result := make([]models.CodingAuth, 0, len(decrypted))
	defaultAssigned := false
	for _, cred := range decrypted {
		codingAuth, ok := buildCodingAuthSummary(cred)
		if !ok {
			continue
		}
		if !defaultAssigned && isRunnableCodingAuthStatus(codingAuth.Status) {
			codingAuth.IsDefault = true
			defaultAssigned = true
		}
		result = append(result, codingAuth)
	}

	return result, nil
}

func (s *OrgCredentialStore) ReorderCodingAuths(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) error {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return fmt.Errorf("org credential store db does not support transactions")
	}

	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for idx, id := range ids {
		tag, execErr := tx.Exec(ctx,
			`UPDATE org_credentials SET priority = @priority, updated_at = now() WHERE id = @id AND org_id = @org_id`,
			pgx.NamedArgs{"priority": idx + 1, "id": id, "org_id": orgID},
		)
		if execErr != nil {
			return fmt.Errorf("reorder coding auth %s: %w", id, execErr)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("coding auth %s not found", id)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.reflectOrgCredentialByID(ctx, orgID, id); err != nil {
			return err
		}
	}
	return nil
}

// withCodingAuthPriority runs fn inside a transaction guarded by a per-org
// advisory lock so that the priority lookup + INSERT pair is serialized
// across concurrent callers in the same org. Without this, two simultaneous
// "Add auth" calls could read the same MAX(priority) and end up with
// duplicate priority numbers — harmless for sorting (created_at breaks ties)
// but confusing in the UI.
//
// fn receives the next priority slot and the active transaction; it must
// perform its INSERT/UPDATE on tx so the work happens under the same lock.
// The lock is held until tx commits or rolls back.
func (s *OrgCredentialStore) withCodingAuthPriority(
	ctx context.Context,
	orgID uuid.UUID,
	fn func(tx pgx.Tx, nextPriority int) error,
) error {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return fmt.Errorf("org credential store db does not support transactions")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin priority transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Per-org advisory lock keyed on a stable namespaced string. Hashed to
	// the bigint argument pg_advisory_xact_lock expects.
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		"coding_auth_priority:"+orgID.String(),
	); err != nil {
		return fmt.Errorf("acquire priority lock: %w", err)
	}

	var nextPriority int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(priority), 0) + 1
		FROM org_credentials
		WHERE org_id = @org_id
		  AND provider IN `+codingAuthProviderSQLList+`
		  AND status != 'disabled'`,
		pgx.NamedArgs{"org_id": orgID},
	).Scan(&nextPriority); err != nil {
		return fmt.Errorf("get next coding auth priority: %w", err)
	}

	if err := fn(tx, nextPriority); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *OrgCredentialStore) CreateCodingAuth(ctx context.Context, orgID uuid.UUID, createdBy *uuid.UUID, input models.CreateCodingAuthInput) (*models.CodingAuth, error) {
	cfg, provider, err := providerConfigForCodingAuthInput(input)
	if err != nil {
		return nil, err
	}

	encrypted, err := s.marshalAndEncrypt(cfg)
	if err != nil {
		return nil, err
	}

	var row models.OrgCredential
	err = s.withCodingAuthPriority(ctx, orgID, func(tx pgx.Tx, nextPriority int) error {
		query := `
			INSERT INTO org_credentials (org_id, provider, label, config, status, priority, created_by)
			VALUES (@org_id, @provider, @label, @config, 'active', @priority, @created_by)
			RETURNING id, org_id, provider, label, config, status, priority, last_verified_at, last_used_at, created_by, created_at, updated_at`

		rows, qerr := tx.Query(ctx, query, pgx.NamedArgs{
			"org_id":     orgID,
			"provider":   string(provider),
			"label":      input.Label,
			"config":     encrypted,
			"priority":   nextPriority,
			"created_by": createdBy,
		})
		if qerr != nil {
			return fmt.Errorf("create coding auth: %w", qerr)
		}
		collected, cerr := pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.OrgCredential])
		if cerr != nil {
			return fmt.Errorf("create coding auth: %w", cerr)
		}
		row = collected
		return nil
	})
	if err != nil {
		return nil, err
	}

	cred, err := s.decryptRow(row)
	if err != nil {
		return nil, err
	}
	codingAuth, ok := buildCodingAuthSummary(*cred)
	if !ok {
		return nil, fmt.Errorf("unsupported coding auth row")
	}
	if err := s.reflectOrgCredentialByID(ctx, orgID, row.ID); err != nil {
		return nil, err
	}
	return &codingAuth, nil
}

func (s *OrgCredentialStore) UpdateCodingAuth(ctx context.Context, orgID uuid.UUID, id uuid.UUID, input models.UpdateCodingAuthInput) (*models.CodingAuth, error) {
	if input.Label == nil {
		return nil, fmt.Errorf("no coding auth fields supplied")
	}

	rows, err := s.db.Query(ctx, `
		UPDATE org_credentials
		SET label = @label, updated_at = now()
		WHERE id = @id AND org_id = @org_id
		RETURNING id, org_id, provider, label, config, status, priority, last_verified_at, last_used_at, created_by, created_at, updated_at`,
		pgx.NamedArgs{"label": *input.Label, "id": id, "org_id": orgID},
	)
	if err != nil {
		return nil, fmt.Errorf("update coding auth: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.OrgCredential])
	if err != nil {
		return nil, fmt.Errorf("update coding auth: %w", err)
	}
	cred, err := s.decryptRow(row)
	if err != nil {
		return nil, err
	}
	codingAuth, ok := buildCodingAuthSummary(*cred)
	if !ok {
		return nil, fmt.Errorf("unsupported coding auth row")
	}
	if err := s.reflectOrgCredentialByID(ctx, orgID, id); err != nil {
		return nil, err
	}
	return &codingAuth, nil
}

func (s *OrgCredentialStore) DisableCodingAuth(ctx context.Context, orgID uuid.UUID, id uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`UPDATE org_credentials SET status = 'disabled', updated_at = now() WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": id, "org_id": orgID},
	)
	if err != nil {
		return fmt.Errorf("disable coding auth: %w", err)
	}
	if mirrErr := s.codingMirror.MirrorOrgCredentialDisable(ctx, orgID, id); mirrErr != nil {
		s.logMirrorFailure("disable-coding-auth", id, mirrErr)
	}
	return nil
}

func (s *OrgCredentialStore) DeleteCodingAuth(ctx context.Context, orgID uuid.UUID, id uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM org_credentials WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": id, "org_id": orgID},
	)
	if err != nil {
		return fmt.Errorf("delete coding auth: %w", err)
	}
	if mirrErr := s.codingMirror.MirrorOrgCredentialDelete(ctx, orgID, id); mirrErr != nil {
		s.logMirrorFailure("delete-coding-auth", id, mirrErr)
	}
	return nil
}

func providerConfigForCodingAuthInput(input models.CreateCodingAuthInput) (models.ProviderConfig, models.ProviderName, error) {
	switch input.Agent {
	case models.AgentTypeCodex:
		return models.OpenAIConfig{
			APIKey:  input.APIKey,
			BaseURL: input.BaseURL,
			APIType: defaultString(input.APIType, "responses"),
		}, models.ProviderOpenAI, nil
	case models.AgentTypeClaudeCode:
		return models.AnthropicConfig{
			APIKey:  input.APIKey,
			BaseURL: input.BaseURL,
		}, models.ProviderAnthropic, nil
	case models.AgentTypeGeminiCLI:
		return models.GeminiConfig{
			APIKey: input.APIKey,
			Model:  defaultString(input.APIType, models.GeminiCLIModelGemini25Pro),
		}, models.ProviderGemini, nil
	case models.AgentTypeAmp:
		return models.AmpConfig{
			APIKey: input.APIKey,
		}, models.ProviderAmp, nil
	case models.AgentTypePi:
		return models.PiConfig{
			APIKey: input.APIKey,
		}, models.ProviderPi, nil
	default:
		return nil, "", fmt.Errorf("unsupported coding auth agent: %s", input.Agent)
	}
}

func buildCodingAuthSummary(cred models.DecryptedCredential) (models.CodingAuth, bool) {
	authType := inferCodingAuthType(cred)
	if authType == "" {
		return models.CodingAuth{}, false
	}

	agent := inferCodingAuthAgent(cred)
	if agent == "" {
		return models.CodingAuth{}, false
	}

	return models.CodingAuth{
		ID:             cred.ID,
		OrgID:          cred.OrgID,
		Priority:       cred.Priority,
		Agent:          agent,
		AuthType:       authType,
		Label:          defaultString(cred.Label, fallbackLabel(agent, authType)),
		Scope:          "organization",
		Provider:       cred.Provider,
		Status:         inferCodingAuthStatus(cred),
		LastVerifiedAt: cred.LastVerifiedAt,
		LastUsedAt:     cred.LastUsedAt,
		UsageNote:      codingAuthUsageNote(cred),
		CreatedBy:      cred.CreatedBy,
		CreatedAt:      cred.CreatedAt,
		UpdatedAt:      cred.UpdatedAt,
	}, true
}

func isSupportedCodingAuthCredential(cred models.DecryptedCredential) bool {
	return inferCodingAuthType(cred) != "" && inferCodingAuthAgent(cred) != ""
}

func inferCodingAuthAgent(cred models.DecryptedCredential) models.AgentType {
	switch cred.Provider {
	case models.ProviderOpenAI, models.ProviderOpenAIChatGPT:
		return models.AgentTypeCodex
	case models.ProviderAnthropic:
		return models.AgentTypeClaudeCode
	case models.ProviderGemini:
		return models.AgentTypeGeminiCLI
	case models.ProviderAmp:
		return models.AgentTypeAmp
	case models.ProviderPi:
		return models.AgentTypePi
	default:
		return ""
	}
}

func inferCodingAuthType(cred models.DecryptedCredential) models.CodingAuthType {
	switch cfg := cred.Config.(type) {
	case models.OpenAIChatGPTConfig:
		return models.CodingAuthTypeSubscription
	case models.OpenAIConfig:
		if cfg.APIKey != "" {
			return models.CodingAuthTypeAPIKey
		}
	case models.AnthropicConfig:
		if cfg.Subscription != nil {
			return models.CodingAuthTypeSubscription
		}
		if cfg.APIKey != "" {
			return models.CodingAuthTypeAPIKey
		}
	case models.GeminiConfig:
		if cfg.APIKey != "" {
			return models.CodingAuthTypeAPIKey
		}
	case models.AmpConfig:
		if cfg.APIKey != "" {
			return models.CodingAuthTypeAPIKey
		}
	case models.PiConfig:
		if cfg.APIKey != "" {
			return models.CodingAuthTypeAPIKey
		}
	}
	return ""
}

func inferCodingAuthStatus(cred models.DecryptedCredential) models.CodingAuthStatus {
	switch cred.Status {
	case "invalid":
		return models.CodingAuthStatusInvalid
	case "pending_auth":
		return models.CodingAuthStatusNeedsReauth
	case "active":
		return models.CodingAuthStatusHealthy
	default:
		return models.CodingAuthStatusNeedsReauth
	}
}

func isRunnableCodingAuthStatus(status models.CodingAuthStatus) bool {
	return status == models.CodingAuthStatusHealthy
}

func codingAuthUsageNote(cred models.DecryptedCredential) string {
	switch cfg := cred.Config.(type) {
	case models.OpenAIChatGPTConfig:
		return defaultString(cfg.AccountType, "ChatGPT subscription")
	case models.OpenAIConfig:
		return cfg.MaskedSummary().MaskedKey
	case models.AnthropicConfig:
		if cfg.Subscription != nil {
			return defaultString(cfg.Subscription.AccountType, "Claude subscription")
		}
		return cfg.MaskedSummary().MaskedKey
	case models.GeminiConfig:
		return cfg.MaskedSummary().MaskedKey
	case models.AmpConfig:
		return cfg.MaskedSummary().MaskedKey
	case models.PiConfig:
		return cfg.MaskedSummary().MaskedKey
	default:
		return ""
	}
}

func fallbackLabel(agent models.AgentType, authType models.CodingAuthType) string {
	switch {
	case agent == models.AgentTypeCodex && authType == models.CodingAuthTypeSubscription:
		return "Codex subscription"
	case agent == models.AgentTypeCodex && authType == models.CodingAuthTypeAPIKey:
		return "Codex API key"
	case agent == models.AgentTypeClaudeCode && authType == models.CodingAuthTypeSubscription:
		return "Claude Code subscription"
	case agent == models.AgentTypeClaudeCode && authType == models.CodingAuthTypeAPIKey:
		return "Claude Code API key"
	case agent == models.AgentTypeGeminiCLI && authType == models.CodingAuthTypeAPIKey:
		return "Gemini CLI API key"
	case agent == models.AgentTypeAmp && authType == models.CodingAuthTypeAPIKey:
		return "Amp API key"
	case agent == models.AgentTypePi && authType == models.CodingAuthTypeAPIKey:
		return "Pi API key"
	default:
		return "Coding auth"
	}
}

func defaultString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
