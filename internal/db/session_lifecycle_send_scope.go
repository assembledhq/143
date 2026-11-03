package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const threadSendVisibleLaneLockQuery = `SELECT id, org_id, session_id, status, archived_at,
	admission_epoch, end_epoch, lifecycle_version FROM session_threads
	WHERE org_id = $1 AND session_id = $2 AND (archived_at IS NULL OR id = $3)
	ORDER BY id LIMIT $4 FOR NO KEY UPDATE NOWAIT`

// RequireLegacyThreadSendScopeInTx is the fixed legacy guard for one complete
// atomic thread-send mutation. The owner invokes it afresh for every attempted
// supplied, configured transaction and discards the whole unit on any error.
// A previous entry-mode observation is not enough.
//
// It acquires the shared cutover interlock, parent, then all visible lanes and
// the explicitly requested lane in stable order. This lets the unchanged legacy
// claim SQL run after canonical acquisition without making that SQL an authority
// check. It installs no settings and owns no retry, settlement or notification.
func RequireLegacyThreadSendScopeInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID, threadID uuid.UUID) error {
	_, err := CaptureLegacyThreadSendScopeInTx(ctx, tx, orgID, sessionID, threadID)
	return err
}

// LegacyThreadSendSnapshot is an observation of rows locked by this attempt,
// not reusable admission authority. Use it only in the same transaction before
// changing the locked rows. Claims must still enforce their SQL predicates.
type LegacyThreadSendSnapshot struct {
	SessionStatus  models.SessionStatus
	SessionDeleted bool
	ThreadStatus   models.ThreadStatus
	ThreadArchived bool
	ActiveSiblings int
}

// CaptureLegacyThreadSendScopeInTx acquires the same fixed legacy guard and
// returns the already-locked status/capacity observations for branch selection.
// This avoids speculative UPDATEs and re-reading rows that cannot change while
// this transaction holds their locks. Every retry must capture a fresh snapshot.
func CaptureLegacyThreadSendScopeInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID, threadID uuid.UUID) (LegacyThreadSendSnapshot, error) {
	if err := validateThreadSendScope(ctx, tx, orgID, sessionID, threadID); err != nil {
		return LegacyThreadSendSnapshot{}, err
	}
	if err := RequireLegacyThreadSendModeInTx(ctx, tx, orgID, sessionID); err != nil {
		return LegacyThreadSendSnapshot{}, err
	}
	locked, err := lockThreadSendScopeInTx(ctx, tx, orgID, sessionID, threadID)
	if err != nil {
		return LegacyThreadSendSnapshot{}, err
	}
	result := LegacyThreadSendSnapshot{SessionStatus: locked.session.status, SessionDeleted: locked.session.deletedAt != nil}
	for _, lane := range locked.threads {
		if lane.id == threadID {
			result.ThreadStatus = lane.status
			result.ThreadArchived = lane.archivedAt != nil
			continue
		}
		if lane.archivedAt == nil && (lane.status == models.ThreadStatusPending || lane.status == models.ThreadStatusRunning || lane.status == models.ThreadStatusAwaitingInput) {
			result.ActiveSiblings++
		}
	}
	return result, nil
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
	// Parent ownership serializes cooperating topology writers. Discover and
	// lock the bounded visible set in the same ordered statement; no unlocked
	// identity list or second round trip is needed. Even refusal discards the
	// entire unit, including any locks acquired by this statement.
	rows, err := tx.Query(ctx, threadSendVisibleLaneLockQuery, pgx.QueryExecModeExec, orgID, sessionID, threadID, limit+1)
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
	if len(lanes) > limit {
		return lockedLifecycleScope{}, ErrLifecycleConsumerDiscoveryIncomplete
	}
	foundTarget := false
	for i, lane := range lanes {
		if lane.id == uuid.Nil || lane.orgID != orgID || lane.sessionID != sessionID ||
			(i > 0 && bytes.Compare(lanes[i-1].id[:], lane.id[:]) >= 0) {
			return lockedLifecycleScope{}, fmt.Errorf("thread send lane scope membership changed: %w", pgx.ErrNoRows)
		}
		foundTarget = foundTarget || lane.id == threadID
	}
	if !foundTarget {
		return lockedLifecycleScope{}, fmt.Errorf("thread send exact lane missing: %w", pgx.ErrNoRows)
	}
	if err := ctx.Err(); err != nil {
		return lockedLifecycleScope{}, fmt.Errorf("thread send scope context after lane locks: %w", err)
	}
	parent.threads = lanes
	return parent, nil
}
