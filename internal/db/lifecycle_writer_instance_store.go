package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

// ErrLifecycleWriterInstanceIdentityMismatch means a process instance identifier
// was already observed with a different immutable role, build, or writer version.
var ErrLifecycleWriterInstanceIdentityMismatch = errors.New("lifecycle writer instance identity mismatch")

// Zero is deliberately observational. No caller may advertise a compatible
// writer version through this passive, partial process registry.
const lifecycleWriterInstanceVersion = 0

type LifecycleWriterInstanceStore struct {
	db DBTX
}

func NewLifecycleWriterInstanceStore(db DBTX) *LifecycleWriterInstanceStore {
	return &LifecycleWriterInstanceStore{db: db}
}

// Observe registers or renews one process instance without changing its identity.
// It uses the database clock and a fixed two-minute observation TTL. Callers must
// bound the attempt; observation success is not lifecycle mutation authority.
// lint:allow-no-orgid reason="global passive process observation contains no tenant-owned data"
func (s *LifecycleWriterInstanceStore) Observe(ctx context.Context, instanceID uuid.UUID, role models.LifecycleWriterRole, buildSHA string) error {
	if instanceID == uuid.Nil {
		return errors.New("observe lifecycle writer instance: instance ID must be nonzero")
	}
	if err := role.Validate(); err != nil {
		return fmt.Errorf("observe lifecycle writer instance: %w", err)
	}
	if strings.TrimSpace(buildSHA) == "" || len(buildSHA) > 128 {
		return errors.New("observe lifecycle writer instance: build SHA must contain non-whitespace text and be at most 128 bytes")
	}

	tag, err := s.db.Exec(ctx, `
		WITH observation AS (
			SELECT statement_timestamp() AS observed_at
		)
		INSERT INTO lifecycle_writer_instances (
			instance_id, role, build_sha, lifecycle_writer_version, last_heartbeat, expires_at
		)
		SELECT $1, $2, $3, $4, observed_at, observed_at + interval '2 minutes'
		FROM observation
		ON CONFLICT (instance_id) DO UPDATE
		SET last_heartbeat = EXCLUDED.last_heartbeat,
			expires_at = EXCLUDED.expires_at
		WHERE lifecycle_writer_instances.role = EXCLUDED.role
		  AND lifecycle_writer_instances.build_sha = EXCLUDED.build_sha
		  AND lifecycle_writer_instances.lifecycle_writer_version = EXCLUDED.lifecycle_writer_version`,
		instanceID, role, buildSHA, lifecycleWriterInstanceVersion)
	if err != nil {
		return fmt.Errorf("observe lifecycle writer instance: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLifecycleWriterInstanceIdentityMismatch
	}
	return nil
}

// DeleteExpired removes at most 1,000 observations expired for more than seven
// days. Row locks skip concurrent observers; the delete rechecks the cutoff.
// No result or expiry establishes complete inventory or drained in-flight work.
// lint:allow-no-orgid reason="bounded global retention removes only expired passive process observations"
func (s *LifecycleWriterInstanceStore) DeleteExpired(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		WITH expired AS (
			SELECT instance_id
			FROM lifecycle_writer_instances
			WHERE expires_at < statement_timestamp() - interval '7 days'
			ORDER BY expires_at, instance_id
			LIMIT 1000
			FOR UPDATE SKIP LOCKED
		)
		DELETE FROM lifecycle_writer_instances AS writer
		USING expired
		WHERE writer.instance_id = expired.instance_id
		  AND writer.expires_at < statement_timestamp() - interval '7 days'`)
	if err != nil {
		return 0, fmt.Errorf("delete expired lifecycle writer instances: %w", err)
	}
	return tag.RowsAffected(), nil
}
