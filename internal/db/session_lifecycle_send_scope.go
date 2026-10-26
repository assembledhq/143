package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const threadSendVisibleLaneKeysQuery = `SELECT id FROM session_threads
	WHERE org_id = $1 AND session_id = $2 AND (archived_at IS NULL OR id = $3)
	ORDER BY id LIMIT $4`

// RequireLegacyThreadSendScopeInTx is the fixed legacy guard for ONE original
// send mutation unit, not the entire multi-unit send choreography. The owner
// must invoke it afresh in every supplied, configured transaction and discard
// the whole unit on any error. A previous entry-mode observation is not enough.
//
// It acquires the shared cutover interlock, parent, then all visible lanes and
// the explicitly requested lane in stable order. This lets the unchanged legacy
// claim SQL run after canonical acquisition without making that SQL an authority
// check. It installs no settings and owns no retry, settlement or notification.
func RequireLegacyThreadSendScopeInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID, threadID uuid.UUID) error {
	if err := validateThreadSendScope(ctx, tx, orgID, sessionID, threadID); err != nil {
		return err
	}
	if err := RequireLegacyThreadSendModeInTx(ctx, tx, orgID, sessionID); err != nil {
		return err
	}
	_, err := lockThreadSendScopeInTx(ctx, tx, orgID, sessionID, threadID)
	return err
}

func validateThreadSendScope(ctx context.Context, tx pgx.Tx, orgID, sessionID, threadID uuid.UUID) error {
	if ctx == nil || nilLifecycleDependency(tx) {
		return errors.New("thread send scope requires a context and supplied transaction")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("thread send scope context: %w", err)
	}
	if orgID == uuid.Nil || sessionID == uuid.Nil || threadID == uuid.Nil {
		return errors.New("thread send scope requires exact nonzero organization, session and lane IDs")
	}
	return nil
}

// lockThreadSendScopeInTx observes raw rows only. The caller must already have
// resolved the required mode and acquired any higher-ranked domain/job locks.
// Visibility, epoch, owner, status and capacity authorization belongs to the
// canonical mutation, not this observation. There is no primary-lane inference.
func lockThreadSendScopeInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID, threadID uuid.UUID) (lockedLifecycleScope, error) {
	if err := validateThreadSendScope(ctx, tx, orgID, sessionID, threadID); err != nil {
		return lockedLifecycleScope{}, err
	}
	// Holding the parent before discovery serializes cooperating admission and
	// topology writers. Full mixed-writer/topology coverage is still G/H's gate.
	parent, err := lockLifecycleScopeInTx(ctx, tx, orgID, lifecycleScope{sessionID: sessionID})
	if err != nil {
		return lockedLifecycleScope{}, err
	}
	limit := defaultLifecycleConsumerLimits().laneAnchors
	rows, err := tx.Query(ctx, threadSendVisibleLaneKeysQuery, orgID, sessionID, threadID, limit+1)
	if err != nil {
		return lockedLifecycleScope{}, fmt.Errorf("discover thread send lane scope: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return lockedLifecycleScope{}, fmt.Errorf("read thread send lane scope: %w", err)
	}
	if len(ids) > limit {
		return lockedLifecycleScope{}, ErrLifecycleConsumerDiscoveryIncomplete
	}
	foundTarget := false
	for i, id := range ids {
		if id == uuid.Nil || (i > 0 && bytes.Compare(ids[i-1][:], id[:]) >= 0) {
			return lockedLifecycleScope{}, errors.New("thread send lane discovery is not an exact ordered set")
		}
		foundTarget = foundTarget || id == threadID
	}
	if !foundTarget {
		return lockedLifecycleScope{}, fmt.Errorf("thread send exact lane missing: %w", pgx.ErrNoRows)
	}
	if err := ctx.Err(); err != nil {
		return lockedLifecycleScope{}, fmt.Errorf("thread send scope context after discovery: %w", err)
	}
	rows, err = tx.Query(ctx, lifecycleThreadScopeLockQuery, orgID, sessionID, ids)
	if err != nil {
		return lockedLifecycleScope{}, fmt.Errorf("lock thread send lane scopes: %w", err)
	}
	lanes, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (lifecycleLockedThread, error) {
		var lane lifecycleLockedThread
		err := row.Scan(&lane.id, &lane.orgID, &lane.sessionID, &lane.status, &lane.archivedAt,
			&lane.epochs.AdmissionEpoch, &lane.epochs.EndEpoch, &lane.epochs.LifecycleVersion)
		return lane, err
	})
	if err != nil {
		return lockedLifecycleScope{}, fmt.Errorf("read locked thread send lane scopes: %w", err)
	}
	if len(lanes) != len(ids) {
		return lockedLifecycleScope{}, fmt.Errorf("thread send lane scope changed: %w", pgx.ErrNoRows)
	}
	for i, lane := range lanes {
		if lane.id != ids[i] || lane.orgID != orgID || lane.sessionID != sessionID {
			return lockedLifecycleScope{}, fmt.Errorf("thread send lane scope membership changed: %w", pgx.ErrNoRows)
		}
	}
	if err := ctx.Err(); err != nil {
		return lockedLifecycleScope{}, fmt.Errorf("thread send scope context after lane locks: %w", err)
	}
	parent.threads = lanes
	return parent, nil
}
