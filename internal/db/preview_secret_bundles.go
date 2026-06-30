package db

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/crypto"
	"github.com/assembledhq/143/internal/models"
)

const previewSecretBundleColumns = "id, org_id, repository_id, name, active, source_type, source_config_encrypted, outputs_config_encrypted, exposure_policy, created_by_user_id, created_at" // #nosec G101 -- SQL column list, not a credential

var ErrPreviewSecretBundleNameConflict = errors.New("preview secret bundle name already exists")

type PreviewSecretBundleStore struct {
	db     DBTX
	crypto *crypto.Service
	keyID  string
}

func NewPreviewSecretBundleStore(db DBTX, cryptoSvc *crypto.Service, keyID string) *PreviewSecretBundleStore {
	if keyID == "" {
		keyID = "preview-secret-bundles-v1"
	}
	return &PreviewSecretBundleStore{db: db, crypto: cryptoSvc, keyID: keyID}
}

type UpsertPreviewSecretBundleInput struct {
	RepositoryID    uuid.UUID
	Name            string
	Source          models.PreviewSecretBundleSource
	Outputs         []models.PreviewSecretBundleOutput
	ExposurePolicy  string
	CreatedByUserID uuid.UUID
}

func (s *PreviewSecretBundleStore) Upsert(ctx context.Context, orgID uuid.UUID, in UpsertPreviewSecretBundleInput) (*models.PreviewSecretBundle, error) {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return nil, fmt.Errorf("preview secret bundle store requires transaction-capable db")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin preview secret bundle upsert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		UPDATE preview_secret_bundles
		SET active = false
		WHERE org_id = @org_id
		  AND repository_id = @repository_id
		  AND name = @name
		  AND active = true`,
		pgx.NamedArgs{"org_id": orgID, "repository_id": in.RepositoryID, "name": in.Name},
	); err != nil {
		return nil, fmt.Errorf("deactivate previous preview secret bundle: %w", err)
	}

	sourceEncrypted, err := s.encryptJSON(in.Source)
	if err != nil {
		return nil, fmt.Errorf("encrypt preview secret bundle source: %w", err)
	}
	outputsEncrypted, err := s.encryptJSON(in.Outputs)
	if err != nil {
		return nil, fmt.Errorf("encrypt preview secret bundle outputs: %w", err)
	}
	exposurePolicy := in.ExposurePolicy
	if exposurePolicy == "" {
		exposurePolicy = "preview_runtime"
	}

	var row models.PreviewSecretBundle
	err = tx.QueryRow(ctx, `
		INSERT INTO preview_secret_bundles (
			org_id, repository_id, name, active, source_type, source_config_encrypted,
			outputs_config_encrypted, exposure_policy, created_by_user_id
		)
		SELECT
			@org_id, r.id, @name, true, @source_type, @source_config_encrypted,
			@outputs_config_encrypted, @exposure_policy, @created_by_user_id
		FROM repositories r
		WHERE r.id = @repository_id
		  AND r.org_id = @org_id
		RETURNING `+previewSecretBundleColumns,
		pgx.NamedArgs{
			"org_id":                   orgID,
			"repository_id":            in.RepositoryID,
			"name":                     in.Name,
			"source_type":              in.Source.Type,
			"source_config_encrypted":  sourceEncrypted,
			"outputs_config_encrypted": outputsEncrypted,
			"exposure_policy":          exposurePolicy,
			"created_by_user_id":       in.CreatedByUserID,
		},
	).Scan(
		&row.ID, &row.OrgID, &row.RepositoryID, &row.Name, &row.Active, &row.SourceType,
		&row.SourceConfigEncrypted, &row.OutputsConfigEncrypted, &row.ExposurePolicy,
		&row.CreatedByUserID, &row.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, err
		}
		return nil, fmt.Errorf("insert preview secret bundle: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit preview secret bundle upsert: %w", err)
	}
	return &row, nil
}

func (s *PreviewSecretBundleStore) ReplaceActiveByID(ctx context.Context, orgID uuid.UUID, id uuid.UUID, in UpsertPreviewSecretBundleInput) (*models.PreviewSecretBundle, error) {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return nil, fmt.Errorf("preview secret bundle store requires transaction-capable db")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin preview secret bundle replace: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	existing, err := getActivePreviewSecretBundleByID(ctx, tx, orgID, id)
	if err != nil {
		return nil, err
	}
	in.RepositoryID = existing.RepositoryID
	if in.Name != existing.Name {
		if _, err := getActivePreviewSecretBundleByName(ctx, tx, orgID, existing.RepositoryID, in.Name); err == nil {
			return nil, ErrPreviewSecretBundleNameConflict
		} else if err != pgx.ErrNoRows {
			return nil, err
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE preview_secret_bundles
		SET active = false
		WHERE org_id = @org_id
		  AND repository_id = @repository_id
		  AND active = true
		  AND id = @id`,
		pgx.NamedArgs{"org_id": orgID, "repository_id": existing.RepositoryID, "id": id},
	); err != nil {
		return nil, fmt.Errorf("deactivate previous preview secret bundle: %w", err)
	}

	row, err := s.insertPreviewSecretBundle(ctx, tx, orgID, in, true)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit preview secret bundle replace: %w", err)
	}
	return row, nil
}

func (s *PreviewSecretBundleStore) GetActive(ctx context.Context, orgID, repositoryID uuid.UUID, name string) (*models.PreviewSecretBundle, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+previewSecretBundleColumns+`
		FROM preview_secret_bundles
		WHERE org_id = @org_id
		  AND repository_id = @repository_id
		  AND name = @name
		  AND active = true`,
		pgx.NamedArgs{"org_id": orgID, "repository_id": repositoryID, "name": name},
	)
	if err != nil {
		return nil, fmt.Errorf("get preview secret bundle: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewSecretBundle])
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *PreviewSecretBundleStore) GetActiveByID(ctx context.Context, orgID, id uuid.UUID) (*models.PreviewSecretBundle, error) {
	row, err := getActivePreviewSecretBundleByID(ctx, s.db, orgID, id)
	if err != nil {
		return nil, err
	}
	return row, nil
}

func (s *PreviewSecretBundleStore) ListActive(ctx context.Context, orgID, repositoryID uuid.UUID) ([]models.PreviewSecretBundle, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+previewSecretBundleColumns+`
		FROM preview_secret_bundles
		WHERE org_id = @org_id
		  AND repository_id = @repository_id
		  AND active = true
		ORDER BY name ASC`,
		pgx.NamedArgs{"org_id": orgID, "repository_id": repositoryID},
	)
	if err != nil {
		return nil, fmt.Errorf("list preview secret bundles: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.PreviewSecretBundle])
}

func (s *PreviewSecretBundleStore) Disable(ctx context.Context, orgID, repositoryID uuid.UUID, name string, userID uuid.UUID) error {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return fmt.Errorf("preview secret bundle store requires transaction-capable db")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin preview secret bundle disable: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	existing, err := getActivePreviewSecretBundleByName(ctx, tx, orgID, repositoryID, name)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE preview_secret_bundles
		SET active = false
		WHERE org_id = @org_id
		  AND id = @id
		  AND active = true`,
		pgx.NamedArgs{"org_id": orgID, "id": existing.ID},
	)
	if err != nil {
		return fmt.Errorf("disable preview secret bundle: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if _, err := insertPreviewSecretBundleRow(ctx, tx, previewSecretBundleInsertInput{
		OrgID:                  orgID,
		RepositoryID:           existing.RepositoryID,
		Name:                   existing.Name,
		Active:                 false,
		SourceType:             existing.SourceType,
		SourceConfigEncrypted:  existing.SourceConfigEncrypted,
		OutputsConfigEncrypted: existing.OutputsConfigEncrypted,
		ExposurePolicy:         existing.ExposurePolicy,
		CreatedByUserID:        userID,
	}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit preview secret bundle disable: %w", err)
	}
	return nil
}

func (s *PreviewSecretBundleStore) DecryptSource(ctx context.Context, orgID uuid.UUID, row models.PreviewSecretBundle) (models.PreviewSecretBundleSource, error) {
	_ = ctx
	var out models.PreviewSecretBundleSource
	if orgID != row.OrgID {
		return out, pgx.ErrNoRows
	}
	if err := s.decryptJSON(row.SourceConfigEncrypted, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (s *PreviewSecretBundleStore) DecryptOutputs(ctx context.Context, orgID uuid.UUID, row models.PreviewSecretBundle) ([]models.PreviewSecretBundleOutput, error) {
	_ = ctx
	if orgID != row.OrgID {
		return nil, pgx.ErrNoRows
	}
	var out []models.PreviewSecretBundleOutput
	if err := s.decryptJSON(row.OutputsConfigEncrypted, &out); err != nil {
		return nil, err
	}
	return out, nil
}

type previewSecretEncryptedBlob struct {
	Alg        string `json:"alg"`
	KID        string `json:"kid,omitempty"`
	Ciphertext string `json:"ciphertext"`
}

func (s *PreviewSecretBundleStore) encryptJSON(v any) (json.RawMessage, error) {
	plaintext, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	alg := "aes-256-gcm-envelope"
	var data []byte
	if s.crypto == nil {
		alg = "dev-plaintext"
		data = crypto.DevEncrypt(plaintext)
	} else {
		data, err = s.crypto.Encrypt(plaintext)
		if err != nil {
			return nil, err
		}
	}
	blob, err := json.Marshal(previewSecretEncryptedBlob{
		Alg:        alg,
		KID:        s.keyID,
		Ciphertext: base64.StdEncoding.EncodeToString(data),
	})
	if err != nil {
		return nil, err
	}
	return blob, nil
}

func (s *PreviewSecretBundleStore) decryptJSON(raw json.RawMessage, out any) error {
	var blob previewSecretEncryptedBlob
	if err := json.Unmarshal(raw, &blob); err != nil {
		return fmt.Errorf("parse encrypted blob: %w", err)
	}
	data, err := base64.StdEncoding.DecodeString(blob.Ciphertext)
	if err != nil {
		return fmt.Errorf("decode encrypted blob: %w", err)
	}
	var plaintext []byte
	switch blob.Alg {
	case "dev-plaintext":
		// dev-plaintext is only ever written when no encryption key is
		// configured (s.crypto == nil), which cannot happen in production
		// because ENCRYPTION_MASTER_KEY is required and is the KEK fallback.
		// If we have a key but encounter a plaintext blob, it is anomalous
		// (e.g. data copied from a dev/staging environment) — refuse it rather
		// than silently consuming an unencrypted secret.
		if s.crypto != nil {
			return fmt.Errorf("refusing to read dev-plaintext preview secret bundle while encryption is configured")
		}
		plaintext, err = crypto.DevDecrypt(data)
	case "aes-256-gcm-envelope":
		if s.crypto == nil {
			return fmt.Errorf("preview secret bundle encryption key is not configured")
		}
		plaintext, err = s.crypto.Decrypt(data)
	default:
		err = fmt.Errorf("unsupported encrypted blob alg %q", blob.Alg)
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(plaintext, out); err != nil {
		return fmt.Errorf("decode decrypted preview secret bundle: %w", err)
	}
	return nil
}

func (s *PreviewSecretBundleStore) insertPreviewSecretBundle(ctx context.Context, tx DBTX, orgID uuid.UUID, in UpsertPreviewSecretBundleInput, active bool) (*models.PreviewSecretBundle, error) {
	sourceEncrypted, err := s.encryptJSON(in.Source)
	if err != nil {
		return nil, fmt.Errorf("encrypt preview secret bundle source: %w", err)
	}
	outputsEncrypted, err := s.encryptJSON(in.Outputs)
	if err != nil {
		return nil, fmt.Errorf("encrypt preview secret bundle outputs: %w", err)
	}
	exposurePolicy := in.ExposurePolicy
	if exposurePolicy == "" {
		exposurePolicy = "preview_runtime"
	}
	return insertPreviewSecretBundleRow(ctx, tx, previewSecretBundleInsertInput{
		OrgID:                  orgID,
		RepositoryID:           in.RepositoryID,
		Name:                   in.Name,
		Active:                 active,
		SourceType:             in.Source.Type,
		SourceConfigEncrypted:  sourceEncrypted,
		OutputsConfigEncrypted: outputsEncrypted,
		ExposurePolicy:         exposurePolicy,
		CreatedByUserID:        in.CreatedByUserID,
	})
}

type previewSecretBundleInsertInput struct {
	OrgID                  uuid.UUID
	RepositoryID           uuid.UUID
	Name                   string
	Active                 bool
	SourceType             string
	SourceConfigEncrypted  json.RawMessage
	OutputsConfigEncrypted json.RawMessage
	ExposurePolicy         string
	CreatedByUserID        uuid.UUID
}

func insertPreviewSecretBundleRow(ctx context.Context, db DBTX, in previewSecretBundleInsertInput) (*models.PreviewSecretBundle, error) {
	var row models.PreviewSecretBundle
	err := db.QueryRow(ctx, `
		INSERT INTO preview_secret_bundles (
			org_id, repository_id, name, active, source_type, source_config_encrypted,
			outputs_config_encrypted, exposure_policy, created_by_user_id
		)
		SELECT
			@org_id, r.id, @name, @active, @source_type, @source_config_encrypted,
			@outputs_config_encrypted, @exposure_policy, @created_by_user_id
		FROM repositories r
		WHERE r.id = @repository_id
		  AND r.org_id = @org_id
		RETURNING `+previewSecretBundleColumns,
		pgx.NamedArgs{
			"org_id":                   in.OrgID,
			"repository_id":            in.RepositoryID,
			"name":                     in.Name,
			"active":                   in.Active,
			"source_type":              in.SourceType,
			"source_config_encrypted":  in.SourceConfigEncrypted,
			"outputs_config_encrypted": in.OutputsConfigEncrypted,
			"exposure_policy":          in.ExposurePolicy,
			"created_by_user_id":       in.CreatedByUserID,
		},
	).Scan(
		&row.ID, &row.OrgID, &row.RepositoryID, &row.Name, &row.Active, &row.SourceType,
		&row.SourceConfigEncrypted, &row.OutputsConfigEncrypted, &row.ExposurePolicy,
		&row.CreatedByUserID, &row.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, err
		}
		return nil, fmt.Errorf("insert preview secret bundle: %w", err)
	}
	return &row, nil
}

func getActivePreviewSecretBundleByName(ctx context.Context, db DBTX, orgID, repositoryID uuid.UUID, name string) (*models.PreviewSecretBundle, error) {
	rows, err := db.Query(ctx, `
		SELECT `+previewSecretBundleColumns+`
		FROM preview_secret_bundles
		WHERE org_id = @org_id
		  AND repository_id = @repository_id
		  AND name = @name
		  AND active = true
		FOR UPDATE`,
		pgx.NamedArgs{"org_id": orgID, "repository_id": repositoryID, "name": name},
	)
	if err != nil {
		return nil, fmt.Errorf("get preview secret bundle: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewSecretBundle])
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func getActivePreviewSecretBundleByID(ctx context.Context, db DBTX, orgID, id uuid.UUID) (*models.PreviewSecretBundle, error) {
	rows, err := db.Query(ctx, `
		SELECT `+previewSecretBundleColumns+`
		FROM preview_secret_bundles
		WHERE org_id = @org_id
		  AND id = @id
		  AND active = true
		FOR UPDATE`,
		pgx.NamedArgs{"org_id": orgID, "id": id},
	)
	if err != nil {
		return nil, fmt.Errorf("get preview secret bundle: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PreviewSecretBundle])
	if err != nil {
		return nil, err
	}
	return &row, nil
}
