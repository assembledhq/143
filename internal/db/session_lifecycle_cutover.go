package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	lifecycleCutoverSharedLockQuery    = `SELECT pg_try_advisory_xact_lock_shared(hashtextextended($1::text, 0))`
	lifecycleCutoverExclusiveLockQuery = `SELECT pg_try_advisory_xact_lock(hashtextextended($1::text, 0))`
	lifecycleCutoverObservationQuery   = `SELECT org_id, session_id, version, state,
		legacy_input_cutoff_id, normalization, activated_at, minimum_writer_version,
		resumability_capability FROM session_lifecycle_cutovers
		WHERE org_id = $1 AND session_id = $2`
)

// lifecycleCutoverObservation is raw passive metadata, not a permit, floor
// certificate, normalized input boundary, capability proof or activation result.
// No production caller is wired. Any future canonical mutation must honor the
// interlock and prove all prerequisites; arbitrary writers are not excluded by
// an advisory lock. These observations cannot escape their owning transaction.
type lifecycleCutoverObservation struct {
	orgID, sessionID       uuid.UUID
	version                int64
	state                  models.LifecycleCutoverState
	legacyInputCutoffID    *int64
	normalization          json.RawMessage
	activatedAt            *time.Time
	minimumWriterVersion   *int
	resumabilityCapability json.RawMessage
}

// lifecycleCutoverInterlockBusyError is created only for an actual false result
// from the private transaction-scoped advisory try-lock. It is not a PostgreSQL
// SQLSTATE or an exhausted/confirmed-aborted unit. This exact private type is
// eligible for the owner's existing bounded retry protocol only after confirmed
// whole rollback and its other exclusions. Acquisition alone never permits replay.
type lifecycleCutoverInterlockBusyError struct{}

func (*lifecycleCutoverInterlockBusyError) Error() string {
	return "lifecycle cutover interlock busy"
}

// observeLifecycleCutoverInTx takes shared interlock before a SEPARATE fresh
// READ COMMITTED lookup. Successful late acquisition continues without a
// manufactured restart. Missing metadata remains ErrNoRows, never inferred V0.
// On every error return no observation; the caller must roll back the whole unit.
// This Go two-statement sequence does not prove a future VOLATILE trigger's
// within-statement snapshot behavior, and it does not install any guard.
func observeLifecycleCutoverInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) (lifecycleCutoverObservation, error) {
	if err := tryLockLifecycleCutoverSharedInTx(ctx, tx, orgID, sessionID); err != nil {
		return lifecycleCutoverObservation{}, err
	}
	var observation lifecycleCutoverObservation
	err := tx.QueryRow(ctx, lifecycleCutoverObservationQuery, orgID, sessionID).
		Scan(&observation.orgID, &observation.sessionID, &observation.version, &observation.state,
			&observation.legacyInputCutoffID, &observation.normalization, &observation.activatedAt,
			&observation.minimumWriterVersion, &observation.resumabilityCapability)
	if err != nil {
		return lifecycleCutoverObservation{}, fmt.Errorf("read lifecycle cutover observation: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return lifecycleCutoverObservation{}, fmt.Errorf("lifecycle cutover context after observation: %w", err)
	}
	if observation.orgID != orgID || observation.sessionID != sessionID {
		return lifecycleCutoverObservation{}, fmt.Errorf("lifecycle cutover observation scope mismatch: %w", pgx.ErrNoRows)
	}
	return observation, nil
}

// tryLockLifecycleCutoverSharedInTx is only the nonwaiting entry interlock, not
// row/domain/owner authority. Its owner retains all settings and settlement.
func tryLockLifecycleCutoverSharedInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) error {
	return tryLockLifecycleCutoverInTx(ctx, tx, orgID, sessionID, false)
}

// tryLockLifecycleCutoverExclusiveInTx does not activate, normalize or stamp
// anything. Future activation additionally requires the complete floor/drain,
// classification and tested-capability gates while holding this same key.
func tryLockLifecycleCutoverExclusiveInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) error {
	return tryLockLifecycleCutoverInTx(ctx, tx, orgID, sessionID, true)
}

func tryLockLifecycleCutoverInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID, exclusive bool) error {
	if ctx == nil {
		return errors.New("lock lifecycle cutover: nil context")
	}
	if nilLifecycleDependency(tx) {
		return errors.New("lock lifecycle cutover: nil transaction")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("lock lifecycle cutover context: %w", err)
	}
	if orgID == uuid.Nil || sessionID == uuid.Nil {
		return errors.New("lock lifecycle cutover: nonzero organization and session IDs required")
	}
	if err := requireLifecycleScopeTransactionInTx(ctx, tx); err != nil {
		return err
	}
	// The namespace and canonical UUID separators are part of the protocol for
	// future SQL guards too. Hash collisions can only add contention; the exact
	// tenant/session lookup remains mandatory and cannot be replaced by the key.
	key := "session_lifecycle_cutover:" + orgID.String() + ":" + sessionID.String()
	query := lifecycleCutoverSharedLockQuery
	if exclusive {
		query = lifecycleCutoverExclusiveLockQuery
	}
	var acquired bool
	if err := tx.QueryRow(ctx, query, key).Scan(&acquired); err != nil {
		return fmt.Errorf("try lifecycle cutover interlock: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("lifecycle cutover context after acquisition: %w", err)
	}
	if !acquired {
		return &lifecycleCutoverInterlockBusyError{}
	}
	return nil
}
