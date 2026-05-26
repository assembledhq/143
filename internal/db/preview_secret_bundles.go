package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/crypto"
	"github.com/assembledhq/143/internal/models"
)

const previewSecretBundleColumns = "id, org_id, name, encrypted_env, created_by, created_at, updated_at" // #nosec G101 -- SQL column list, not credentials

type previewSecretBundleRow struct {
	ID           uuid.UUID  `db:"id"`
	OrgID        uuid.UUID  `db:"org_id"`
	Name         string     `db:"name"`
	EncryptedEnv []byte     `db:"encrypted_env"`
	CreatedBy    *uuid.UUID `db:"created_by"`
	CreatedAt    time.Time  `db:"created_at"`
	UpdatedAt    time.Time  `db:"updated_at"`
}

// PreviewSecretBundleStore manages admin-provided preview environment bundles.
// Values are encrypted at rest and only decrypted for preview runtime injection.
type PreviewSecretBundleStore struct {
	db     DBTX
	crypto *crypto.Service
}

func NewPreviewSecretBundleStore(db DBTX, cryptoSvc *crypto.Service) *PreviewSecretBundleStore {
	return &PreviewSecretBundleStore{db: db, crypto: cryptoSvc}
}

func (s *PreviewSecretBundleStore) UpsertEnv(ctx context.Context, orgID, createdBy uuid.UUID, name string, env map[string]string) error {
	input := models.PreviewSecretBundleInput{Name: name, Env: env}.Normalized()
	if err := input.Validate(); err != nil {
		return err
	}
	encrypted, err := s.marshalAndEncrypt(input.Env)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO preview_secret_bundles (org_id, name, encrypted_env, created_by)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (org_id, name)
		DO UPDATE SET encrypted_env = EXCLUDED.encrypted_env, updated_at = now()
	`, orgID, input.Name, encrypted, createdBy)
	if err != nil {
		return fmt.Errorf("upsert preview secret bundle: %w", err)
	}
	return nil
}

func (s *PreviewSecretBundleStore) GetEnv(ctx context.Context, orgID uuid.UUID, name string) (map[string]string, error) {
	row, err := s.getRow(ctx, orgID, name)
	if err != nil {
		return nil, err
	}
	env, err := s.decryptEnv(row.EncryptedEnv)
	if err != nil {
		return nil, fmt.Errorf("decrypt preview secret bundle %q: %w", name, err)
	}
	return env, nil
}

func (s *PreviewSecretBundleStore) ListSummaries(ctx context.Context, orgID uuid.UUID) ([]models.PreviewSecretBundleSummary, error) {
	rows, err := s.db.Query(ctx, `SELECT `+previewSecretBundleColumns+` FROM preview_secret_bundles WHERE org_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, fmt.Errorf("query preview secret bundles: %w", err)
	}
	dbRows, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[previewSecretBundleRow])
	if err != nil {
		return nil, fmt.Errorf("collect preview secret bundles: %w", err)
	}
	out := make([]models.PreviewSecretBundleSummary, 0, len(dbRows))
	for _, row := range dbRows {
		env, decErr := s.decryptEnv(row.EncryptedEnv)
		if decErr != nil {
			return nil, fmt.Errorf("decrypt preview secret bundle %q: %w", row.Name, decErr)
		}
		out = append(out, models.PreviewSecretBundleSummary{
			ID:        row.ID,
			Name:      row.Name,
			EnvNames:  models.PreviewSecretEnvNames(env),
			CreatedBy: row.CreatedBy,
			CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt,
		})
	}
	return out, nil
}

func (s *PreviewSecretBundleStore) Delete(ctx context.Context, orgID uuid.UUID, name string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM preview_secret_bundles WHERE org_id = $1 AND name = $2`, orgID, name)
	if err != nil {
		return fmt.Errorf("delete preview secret bundle: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *PreviewSecretBundleStore) getRow(ctx context.Context, orgID uuid.UUID, name string) (previewSecretBundleRow, error) {
	rows, err := s.db.Query(ctx, `SELECT `+previewSecretBundleColumns+` FROM preview_secret_bundles WHERE org_id = $1 AND name = $2`, orgID, name)
	if err != nil {
		return previewSecretBundleRow{}, fmt.Errorf("query preview secret bundle: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[previewSecretBundleRow])
	if err != nil {
		return previewSecretBundleRow{}, fmt.Errorf("get preview secret bundle: %w", err)
	}
	return row, nil
}

func (s *PreviewSecretBundleStore) marshalAndEncrypt(env map[string]string) ([]byte, error) {
	plaintext, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal preview secret env: %w", err)
	}
	encrypted, err := s.encrypt(plaintext)
	if err != nil {
		return nil, fmt.Errorf("encrypt preview secret env: %w", err)
	}
	return encrypted, nil
}

func (s *PreviewSecretBundleStore) decryptEnv(data []byte) (map[string]string, error) {
	plaintext, err := s.decrypt(data)
	if err != nil {
		return nil, err
	}
	var env map[string]string
	if err := json.Unmarshal(plaintext, &env); err != nil {
		return nil, fmt.Errorf("parse preview secret env: %w", err)
	}
	return env, nil
}

func (s *PreviewSecretBundleStore) encrypt(plaintext []byte) ([]byte, error) {
	if s.crypto != nil {
		return s.crypto.Encrypt(plaintext)
	}
	return crypto.DevEncrypt(plaintext), nil
}

func (s *PreviewSecretBundleStore) decrypt(data []byte) ([]byte, error) {
	if s.crypto != nil {
		return s.crypto.Decrypt(data)
	}
	return crypto.DevDecrypt(data)
}
