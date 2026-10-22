package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	lifecycleSessionScopeLockQuery = `SELECT id, org_id, status, deleted_at, archived_at,
		admission_epoch, end_epoch, lifecycle_version
		FROM sessions WHERE org_id = $1 AND id = $2
		FOR NO KEY UPDATE NOWAIT`
	lifecycleThreadScopeLockQuery = `SELECT id, org_id, session_id, status, archived_at,
		admission_epoch, end_epoch, lifecycle_version
		FROM session_threads
		WHERE org_id = $1 AND session_id = $2 AND id = ANY($3::uuid[])
		ORDER BY id FOR NO KEY UPDATE NOWAIT`
)

// lifecycleScope names only explicitly selected rows. An empty lane set means
// session-only locking, never discovery or normalization to a primary lane.
type lifecycleScope struct {
	sessionID uuid.UUID
	threadIDs []uuid.UUID
}

type lifecycleLockedSession struct {
	id, orgID             uuid.UUID
	status                models.SessionStatus
	deletedAt, archivedAt *time.Time
	epochs                LifecycleEpochState
}

type lifecycleLockedThread struct {
	id, orgID, sessionID uuid.UUID
	status               models.ThreadStatus
	archivedAt           *time.Time
	epochs               LifecycleEpochState
}

// lockedLifecycleScope is a private transaction-local observation, not an
// eligibility result, permission, reusable fence or proof of ownership. Its raw
// statuses, visibility markers and epochs still need mandatory canonical checks
// together with mutation before any future production caller can use them.
type lockedLifecycleScope struct {
	session lifecycleLockedSession
	threads []lifecycleLockedThread
}

// lockLifecycleScopeInTx implements only admission-strength row acquisition.
// The owner must have established the lifecycle budget before acquisition and
// configured the transaction. Domain/job locks of higher rank must already be
// held; this helper neither discovers them nor implements the cutover interlock.
// It acquires the exact parent before one stable-order batch of explicit lanes,
// without filtering hidden/terminal rows or inferring expected epochs.
//
// No production caller is wired yet. The helper never owns settlement, retries,
// settings or effects. On ANY error its owner must roll back the entire unit and
// discard all observations; a missing lane does not release earlier locks by
// itself. End/reset must use a separately proved stronger lock, not this helper.
func lockLifecycleScopeInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, scope lifecycleScope) (lockedLifecycleScope, error) {
	if ctx == nil {
		return lockedLifecycleScope{}, errors.New("lock lifecycle scope: nil context")
	}
	if nilLifecycleDependency(tx) {
		return lockedLifecycleScope{}, errors.New("lock lifecycle scope: nil transaction")
	}
	if err := ctx.Err(); err != nil {
		return lockedLifecycleScope{}, fmt.Errorf("lock lifecycle scope context: %w", err)
	}
	if orgID == uuid.Nil || scope.sessionID == uuid.Nil {
		return lockedLifecycleScope{}, errors.New("lock lifecycle scope: nonzero organization and session IDs required")
	}
	threadIDs := slices.Clone(scope.threadIDs)
	for _, id := range threadIDs {
		if id == uuid.Nil {
			return lockedLifecycleScope{}, errors.New("lock lifecycle scope: nonzero explicit thread IDs required")
		}
	}
	slices.SortFunc(threadIDs, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	threadIDs = slices.Compact(threadIDs)
	if err := requireLifecycleScopeTransactionInTx(ctx, tx); err != nil {
		return lockedLifecycleScope{}, err
	}

	var session lifecycleLockedSession
	err := tx.QueryRow(ctx, lifecycleSessionScopeLockQuery, orgID, scope.sessionID).
		Scan(&session.id, &session.orgID, &session.status, &session.deletedAt, &session.archivedAt,
			&session.epochs.AdmissionEpoch, &session.epochs.EndEpoch, &session.epochs.LifecycleVersion)
	if err != nil {
		return lockedLifecycleScope{}, fmt.Errorf("lock lifecycle session scope: %w", err)
	}
	if session.id != scope.sessionID || session.orgID != orgID {
		return lockedLifecycleScope{}, fmt.Errorf("lock lifecycle session membership: %w", pgx.ErrNoRows)
	}
	if err := ctx.Err(); err != nil {
		return lockedLifecycleScope{}, fmt.Errorf("lock lifecycle scope context after session read: %w", err)
	}
	locked := lockedLifecycleScope{session: session, threads: []lifecycleLockedThread{}}
	if len(threadIDs) == 0 {
		return locked, nil
	}
	rows, err := tx.Query(ctx, lifecycleThreadScopeLockQuery, orgID, scope.sessionID, threadIDs)
	if err != nil {
		return lockedLifecycleScope{}, fmt.Errorf("lock lifecycle thread scopes: %w", err)
	}
	threads, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (lifecycleLockedThread, error) {
		var thread lifecycleLockedThread
		err := row.Scan(&thread.id, &thread.orgID, &thread.sessionID, &thread.status, &thread.archivedAt,
			&thread.epochs.AdmissionEpoch, &thread.epochs.EndEpoch, &thread.epochs.LifecycleVersion)
		return thread, err
	})
	if err != nil {
		return lockedLifecycleScope{}, fmt.Errorf("read locked lifecycle thread scopes: %w", err)
	}
	if len(threads) != len(threadIDs) {
		return lockedLifecycleScope{}, fmt.Errorf("lock complete lifecycle thread scopes: %w", pgx.ErrNoRows)
	}
	for index, thread := range threads {
		if thread.id != threadIDs[index] || thread.orgID != orgID || thread.sessionID != scope.sessionID {
			return lockedLifecycleScope{}, fmt.Errorf("lock lifecycle thread membership: %w", pgx.ErrNoRows)
		}
	}
	if err := ctx.Err(); err != nil {
		return lockedLifecycleScope{}, fmt.Errorf("lock lifecycle scope context after thread read: %w", err)
	}
	locked.threads = threads
	return locked, nil
}
