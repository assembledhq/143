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

// UserCredentialStore handles encrypted per-user credential storage.
//
// Like OrgCredentialStore, every coding-provider write is mirrored into the
// unified `coding_credentials` table during the migration window so the
// resolver can read from a single source of truth. SetCodingMirror installs
// the mirror after construction; the cleanup PR removes the mirror entirely.
type UserCredentialStore struct {
	db           DBTX
	crypto       *crypto.Service
	codingMirror CodingCredentialMirror
	mirrorLogf   func(format string, args ...any)
}

// NewUserCredentialStore creates a new user credential store.
func NewUserCredentialStore(db DBTX, cryptoSvc *crypto.Service) *UserCredentialStore {
	return &UserCredentialStore{db: db, crypto: cryptoSvc, codingMirror: NoopMirror()}
}

// SetCodingMirror installs the unified-credentials mirror.
//
// lint:allow-no-orgid reason="process-wide mirror configuration; not tenant data"
func (s *UserCredentialStore) SetCodingMirror(m CodingCredentialMirror) {
	if m == nil {
		s.codingMirror = NoopMirror()
		return
	}
	s.codingMirror = m
}

// SetMirrorLogger installs a logger hook for mirror failures.
//
// lint:allow-no-orgid reason="process-wide logger configuration; not tenant data"
func (s *UserCredentialStore) SetMirrorLogger(logf func(format string, args ...any)) {
	s.mirrorLogf = logf
}

func (s *UserCredentialStore) logMirrorFailure(action string, id uuid.UUID, err error) {
	if s.mirrorLogf == nil || err == nil {
		return
	}
	s.mirrorLogf("coding_credentials user mirror %s id=%s err=%v", action, id, err)
}

// reflectUserCredentialByID re-loads a user_credentials row and asks the
// mirror to reflect it. Same pattern as OrgCredentialStore.reflectOrgCredentialByID.
func (s *UserCredentialStore) reflectUserCredentialByID(ctx context.Context, orgID, id uuid.UUID) error {
	if s.codingMirror == nil || isNoopCodingCredentialMirror(s.codingMirror) {
		return nil
	}
	row, cfg, err := s.loadUserCredentialByID(ctx, orgID, id)
	if err != nil {
		s.logMirrorFailure("load-by-id", id, err)
		return fmt.Errorf("load user credential for versioned mirror: %w", err)
	}
	if mirrErr := s.codingMirror.MirrorUserCredential(ctx, row, cfg); mirrErr != nil {
		s.logMirrorFailure("upsert", id, mirrErr)
		return fmt.Errorf("mirror user credential into versioned coding_credentials: %w", mirrErr)
	}
	return nil
}

func (s *UserCredentialStore) loadUserCredentialByID(ctx context.Context, orgID, id uuid.UUID) (models.UserCredential, models.ProviderConfig, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, user_id, org_id, provider, config, is_team_default, status, last_verified_at, created_at, updated_at
		 FROM user_credentials WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": id, "org_id": orgID},
	)
	if err != nil {
		return models.UserCredential{}, nil, fmt.Errorf("load user credential: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.UserCredential])
	if err != nil {
		return models.UserCredential{}, nil, fmt.Errorf("load user credential row: %w", err)
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
		ON CONFLICT (org_id, user_id, provider)
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
	return s.reflectUserCredentialByID(ctx, orgID, id)
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

// Disable soft-deletes a user credential, also clearing is_team_default.
func (s *UserCredentialStore) Disable(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) error {
	rows, err := s.db.Query(ctx,
		`UPDATE user_credentials SET status = 'disabled', is_team_default = false, updated_at = now()
		 WHERE org_id = @org_id AND user_id = @user_id AND provider = @provider
		 RETURNING id`,
		pgx.NamedArgs{"org_id": orgID, "user_id": userID, "provider": string(provider)},
	)
	if err != nil {
		return err
	}
	// Pass orgID/userID/provider so the mirror can also clean up any
	// team-default row minted by the SQL data-copy migration (which uses a
	// fresh uuid, not the legacy id) — without that cascade the team-default
	// row in coding_credentials would outlive the disabled legacy row.
	for _, id := range collectMirrorIDs(rows) {
		if mirrErr := s.codingMirror.MirrorUserCredentialDisable(ctx, id, orgID, userID, provider); mirrErr != nil {
			s.logMirrorFailure("disable", id, mirrErr)
			return fmt.Errorf("mirror disabled user credential into versioned coding_credentials: %w", mirrErr)
		}
	}
	return nil
}

// ClearTeamDefault unsets is_team_default for a provider across all users in the org.
func (s *UserCredentialStore) ClearTeamDefault(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) error {
	rows, err := s.db.Query(ctx,
		`UPDATE user_credentials SET is_team_default = false, updated_at = now()
		 WHERE org_id = @org_id AND provider = @provider AND is_team_default = true
		 RETURNING id`,
		pgx.NamedArgs{"org_id": orgID, "provider": string(provider)},
	)
	if err != nil {
		return err
	}
	// Was a team-default mirror (org-scoped row in coding_credentials); reflect
	// the new is_team_default=false state so the mirror flips it back to a
	// personal row.
	for _, id := range collectMirrorIDs(rows) {
		if err := s.reflectUserCredentialByID(ctx, orgID, id); err != nil {
			return err
		}
	}
	return nil
}

// SetTeamDefault sets a user's credential as the team default for a provider,
// clearing any existing team default for that provider in the org.
// Both operations run in a single transaction to prevent races.
func (s *UserCredentialStore) SetTeamDefault(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) error {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return fmt.Errorf("user credential store db does not support transactions")
	}

	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Clear existing team default within the transaction and keep the affected
	// ids so the post-commit mirror can re-parent those unified rows back to
	// personal scope.
	clearQuery := `UPDATE user_credentials SET is_team_default = false, updated_at = now()
		WHERE org_id = @org_id AND provider = @provider AND is_team_default = true
		RETURNING id`
	clearRows, err := tx.Query(ctx, clearQuery, pgx.NamedArgs{
		"org_id":   orgID,
		"provider": string(provider),
	})
	if err != nil {
		return fmt.Errorf("clear team default: %w", err)
	}
	clearedIDs := collectMirrorIDs(clearRows)

	// Set the new team default. RETURNING surfaces the affected id so the
	// mirror can flip its scope from personal → org.
	setQuery := `UPDATE user_credentials SET is_team_default = true, updated_at = now()
		WHERE org_id = @org_id AND user_id = @user_id AND provider = @provider AND status = 'active'
		RETURNING id`
	var setID uuid.UUID
	if scanErr := tx.QueryRow(ctx, setQuery, pgx.NamedArgs{
		"org_id":   orgID,
		"user_id":  userID,
		"provider": string(provider),
	}).Scan(&setID); scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return fmt.Errorf("no active %s credential found for user", provider)
		}
		return fmt.Errorf("set team default: %w", scanErr)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for _, clearedID := range clearedIDs {
		if err := s.reflectUserCredentialByID(ctx, orgID, clearedID); err != nil {
			return err
		}
	}
	if err := s.reflectUserCredentialByID(ctx, orgID, setID); err != nil {
		return err
	}
	return nil
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
