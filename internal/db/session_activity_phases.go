package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/assembledhq/143/internal/models"
)

var (
	ErrActivityPhaseConflict = errors.New("activity phase lifecycle conflict")
	ErrInboxBatchConflict    = errors.New("inbox delivery batch lifecycle conflict")
)

const activityPhaseColumns = `id, org_id, session_id, thread_id, turn_number, phase_number,
	status, boundary_reason, started_at, completed_at, runtime_id, trigger_kind,
	trigger_batch_id, trigger_sequence_start, trigger_sequence_end, created_at, updated_at`

const inboxBatchColumns = `id, org_id, session_id, thread_id, runtime_id, sequence_start,
	sequence_end, status, acknowledged_at, started_at, abandoned_at, created_at, updated_at`
const inboxBatchColumnsB = `b.id, b.org_id, b.session_id, b.thread_id, b.runtime_id, b.sequence_start,
	b.sequence_end, b.status, b.acknowledged_at, b.started_at, b.abandoned_at, b.created_at, b.updated_at`

type SessionActivityPhaseStore struct {
	db TxStarter
}

func NewSessionActivityPhaseStore(database TxStarter) *SessionActivityPhaseStore {
	return &SessionActivityPhaseStore{db: database}
}

func (s *SessionActivityPhaseStore) StartPhase(ctx context.Context, orgID, sessionID, threadID uuid.UUID, turnNumber int, runtimeID, runtimeLeaseToken *uuid.UUID, trigger models.ActivityPhaseTrigger, startedAt time.Time) (models.SessionActivityPhase, error) {
	startedAt = postgresTimestamp(startedAt)
	if err := trigger.Kind.Validate(); err != nil {
		return models.SessionActivityPhase{}, err
	}
	if turnNumber < 0 || startedAt.IsZero() {
		return models.SessionActivityPhase{}, fmt.Errorf("start activity phase: invalid turn or start time")
	}
	if (trigger.Kind == models.ActivityPhaseTriggerInboxBatch) != (trigger.BatchID != nil) {
		return models.SessionActivityPhase{}, fmt.Errorf("start activity phase: inbox trigger requires exactly one batch id")
	}
	if trigger.Kind != models.ActivityPhaseTriggerInboxBatch && (runtimeID == nil) != (runtimeLeaseToken == nil) {
		return models.SessionActivityPhase{}, fmt.Errorf("start activity phase: runtime id and lease token must be supplied together")
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return models.SessionActivityPhase{}, fmt.Errorf("begin start activity phase: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lockedThread uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id FROM session_threads
		WHERE org_id = $1 AND session_id = $2 AND id = $3
		FOR UPDATE`, orgID, sessionID, threadID).Scan(&lockedThread); err != nil {
		return models.SessionActivityPhase{}, fmt.Errorf("lock activity phase thread: %w", err)
	}

	var sequenceStart, sequenceEnd *int64
	if trigger.BatchID != nil {
		var batch models.ThreadInboxDeliveryBatch
		rows, queryErr := tx.Query(ctx, `
			UPDATE thread_inbox_delivery_batches
			SET status = 'started', started_at = $5, updated_at = now()
			WHERE org_id = $1 AND id = $2 AND session_id = $3 AND thread_id = $4
			  AND status = 'acknowledged'
			RETURNING `+inboxBatchColumns, orgID, *trigger.BatchID, sessionID, threadID, startedAt)
		if queryErr != nil {
			return models.SessionActivityPhase{}, fmt.Errorf("start inbox delivery batch: %w", queryErr)
		}
		batch, queryErr = pgx.CollectOneRow(rows, pgx.RowToStructByName[models.ThreadInboxDeliveryBatch])
		if queryErr != nil {
			if errors.Is(queryErr, pgx.ErrNoRows) {
				return models.SessionActivityPhase{}, ErrInboxBatchConflict
			}
			return models.SessionActivityPhase{}, fmt.Errorf("collect started inbox delivery batch: %w", queryErr)
		}
		runtimeID = &batch.RuntimeID
		sequenceStart, sequenceEnd = &batch.SequenceStart, &batch.SequenceEnd
		if runtimeLeaseToken == nil {
			return models.SessionActivityPhase{}, fmt.Errorf("start activity phase: inbox trigger requires runtime lease token")
		}
		tag, updateErr := tx.Exec(ctx, `
			UPDATE thread_inbox_entries
			SET applied_at = COALESCE(applied_at, $7), updated_at = now()
			WHERE org_id = $1 AND session_id = $2 AND thread_id = $3 AND runtime_id = $4
			  AND sequence_no BETWEEN $5 AND $6 AND delivery_state = 'acked'`,
			orgID, sessionID, threadID, batch.RuntimeID, batch.SequenceStart, batch.SequenceEnd, startedAt)
		if updateErr != nil {
			return models.SessionActivityPhase{}, fmt.Errorf("mark inbox delivery batch entries applied: %w", updateErr)
		}
		if tag.RowsAffected() != batch.SequenceEnd-batch.SequenceStart+1 {
			return models.SessionActivityPhase{}, ErrInboxBatchConflict
		}
	}
	if runtimeID != nil {
		var ownedRuntime uuid.UUID
		if err := tx.QueryRow(ctx, `
			SELECT id FROM thread_runtimes
			WHERE org_id = $1 AND session_id = $2 AND thread_id = $3 AND id = $4
			  AND lease_token = $5 AND status IN ('starting', 'live', 'paused')
			  AND lease_expires_at IS NOT NULL AND lease_expires_at > $6
			FOR UPDATE`,
			orgID, sessionID, threadID, *runtimeID, *runtimeLeaseToken, startedAt).Scan(&ownedRuntime); err != nil {
			return models.SessionActivityPhase{}, fmt.Errorf("validate active activity phase runtime lease: %w", err)
		}
	}

	rows, err := tx.Query(ctx, `
		INSERT INTO session_activity_phases (
			org_id, session_id, thread_id, turn_number, phase_number, status,
			started_at, runtime_id, trigger_kind, trigger_batch_id,
			trigger_sequence_start, trigger_sequence_end
		)
		SELECT $1, $2, $3, $4, COALESCE(MAX(phase_number), 0) + 1, 'running',
			$5, $6, $7, $8, $9, $10
		FROM session_activity_phases
		WHERE org_id = $1 AND thread_id = $3 AND turn_number = $4
		RETURNING `+activityPhaseColumns,
		orgID, sessionID, threadID, turnNumber, startedAt, runtimeID, trigger.Kind,
		trigger.BatchID, sequenceStart, sequenceEnd)
	if err != nil {
		return models.SessionActivityPhase{}, fmt.Errorf("insert activity phase: %w", err)
	}
	phase, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionActivityPhase])
	if err != nil {
		return models.SessionActivityPhase{}, fmt.Errorf("collect inserted activity phase: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return models.SessionActivityPhase{}, fmt.Errorf("commit start activity phase: %w", err)
	}
	return phase, nil
}

func (s *SessionActivityPhaseStore) CompletePhase(ctx context.Context, orgID, phaseID uuid.UUID, status models.ActivityPhaseStatus, reason models.ActivityPhaseBoundaryReason, completedAt time.Time) (models.SessionActivityPhase, error) {
	phase, _, err := s.CompletePhaseWithTransition(ctx, orgID, phaseID, status, reason, completedAt)
	return phase, err
}

func (s *SessionActivityPhaseStore) CompletePhaseWithTransition(ctx context.Context, orgID, phaseID uuid.UUID, status models.ActivityPhaseStatus, reason models.ActivityPhaseBoundaryReason, completedAt time.Time) (models.SessionActivityPhase, bool, error) {
	completedAt = postgresTimestamp(completedAt)
	if err := validateTerminalActivityPhase(status, reason, completedAt); err != nil {
		return models.SessionActivityPhase{}, false, err
	}
	rows, err := s.db.Query(ctx, `
		UPDATE session_activity_phases
		SET status = $3, boundary_reason = $4, completed_at = $5, updated_at = now()
		WHERE org_id = $1 AND id = $2 AND status = 'running' AND started_at <= $5
		RETURNING `+activityPhaseColumns, orgID, phaseID, status, reason, completedAt)
	if err != nil {
		return models.SessionActivityPhase{}, false, fmt.Errorf("complete activity phase: %w", err)
	}
	phase, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionActivityPhase])
	if err == nil {
		return phase, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return models.SessionActivityPhase{}, false, fmt.Errorf("collect completed activity phase: %w", err)
	}

	rows, err = s.db.Query(ctx, `SELECT `+activityPhaseColumns+`
		FROM session_activity_phases WHERE org_id = $1 AND id = $2`, orgID, phaseID)
	if err != nil {
		return models.SessionActivityPhase{}, false, fmt.Errorf("get terminal activity phase: %w", err)
	}
	existing, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionActivityPhase])
	if err != nil {
		return models.SessionActivityPhase{}, false, fmt.Errorf("collect terminal activity phase: %w", err)
	}
	if existing.Status == status && existing.BoundaryReason == reason &&
		existing.CompletedAt != nil && existing.CompletedAt.Equal(completedAt) {
		return existing, false, nil
	}
	return models.SessionActivityPhase{}, false, ErrActivityPhaseConflict
}

func (s *SessionActivityPhaseStore) ListStrandedRunning(ctx context.Context, orgID uuid.UUID, leaseExpiredBefore time.Time, limit int) ([]models.SessionActivityPhase, error) {
	if limit <= 0 || limit > 500 {
		return nil, fmt.Errorf("list stranded activity phases: limit must be between 1 and 500")
	}
	rows, err := s.db.Query(ctx, `SELECT p.id, p.org_id, p.session_id, p.thread_id, p.turn_number, p.phase_number,
		p.status, p.boundary_reason, p.started_at, p.completed_at, p.runtime_id, p.trigger_kind,
		p.trigger_batch_id, p.trigger_sequence_start, p.trigger_sequence_end, p.created_at, p.updated_at
		FROM session_activity_phases p
		LEFT JOIN thread_runtimes r ON r.org_id = p.org_id AND r.id = p.runtime_id
		JOIN session_threads t ON t.org_id = p.org_id AND t.session_id = p.session_id AND t.id = p.thread_id
		WHERE p.org_id = $1 AND p.status = 'running'
		  AND ((p.runtime_id IS NULL AND p.started_at < $2 AND t.status NOT IN ('pending', 'running'))
		       OR (p.runtime_id IS NOT NULL AND (
		           r.id IS NULL OR r.status IN ('lost', 'closed', 'failed')
		           OR r.lease_expires_at IS NULL OR r.lease_expires_at < $2)))
		ORDER BY p.started_at, p.id
		LIMIT $3`, orgID, leaseExpiredBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("list stranded activity phases: %w", err)
	}
	phases, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionActivityPhase])
	if err != nil {
		return nil, fmt.Errorf("collect stranded activity phases: %w", err)
	}
	return phases, nil
}

// ListStrandedRunningAcrossOrgs scans a bounded set of stranded phases for the system reaper.
// lint:allow-no-orgid reason="system reaper scans bounded stranded phases across organizations"
func (s *SessionActivityPhaseStore) ListStrandedRunningAcrossOrgs(ctx context.Context, leaseExpiredBefore time.Time, limit int) ([]models.SessionActivityPhase, error) {
	if limit <= 0 || limit > 500 {
		return nil, fmt.Errorf("list stranded activity phases across orgs: limit must be between 1 and 500")
	}
	rows, err := s.db.Query(ctx, `SELECT p.id, p.org_id, p.session_id, p.thread_id, p.turn_number, p.phase_number,
		p.status, p.boundary_reason, p.started_at, p.completed_at, p.runtime_id, p.trigger_kind,
		p.trigger_batch_id, p.trigger_sequence_start, p.trigger_sequence_end, p.created_at, p.updated_at
		FROM session_activity_phases p
		LEFT JOIN thread_runtimes r ON r.org_id = p.org_id AND r.id = p.runtime_id
		JOIN session_threads t ON t.org_id = p.org_id AND t.session_id = p.session_id AND t.id = p.thread_id
		WHERE p.status = 'running'
		  AND ((p.runtime_id IS NULL AND p.started_at < $1 AND t.status NOT IN ('pending', 'running'))
		       OR (p.runtime_id IS NOT NULL AND (
		           r.id IS NULL OR r.status IN ('lost', 'closed', 'failed')
		           OR r.lease_expires_at IS NULL OR r.lease_expires_at < $1)))
		ORDER BY p.started_at, p.id
		LIMIT $2`, leaseExpiredBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("list stranded activity phases across orgs: %w", err)
	}
	phases, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionActivityPhase])
	if err != nil {
		return nil, fmt.Errorf("collect stranded activity phases across orgs: %w", err)
	}
	return phases, nil
}

func (s *SessionActivityPhaseStore) ReconcileStrandedPhase(ctx context.Context, orgID, phaseID uuid.UUID, leaseExpiredBefore, completedAt time.Time) (bool, error) {
	completedAt = postgresTimestamp(completedAt)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin reconcile stranded activity phase: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var threadID uuid.UUID
	var runtimeValue any
	if err := tx.QueryRow(ctx, `
		SELECT thread_id, runtime_id
		FROM session_activity_phases
		WHERE org_id = $1 AND id = $2 AND status = 'running'`,
		orgID, phaseID).Scan(&threadID, &runtimeValue); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("get stranded activity phase lock identity: %w", err)
	}

	runtimeID, err := nullableUUID(runtimeValue)
	if err != nil {
		return false, fmt.Errorf("get stranded activity phase lock identity: %w", err)
	}

	var threadStatus models.ThreadStatus
	var runtimeStatus models.ThreadRuntimeStatus
	var leaseExpiresAt pgtype.Timestamptz
	runtimeMissing := false
	if runtimeID == nil {
		if err := tx.QueryRow(ctx, `
			SELECT status FROM session_threads
			WHERE org_id = $1 AND id = $2
			FOR UPDATE`, orgID, threadID).Scan(&threadStatus); err != nil {
			return false, fmt.Errorf("lock runtime-less activity phase thread: %w", err)
		}
	} else {
		err := tx.QueryRow(ctx, `
			SELECT status, lease_expires_at FROM thread_runtimes
			WHERE org_id = $1 AND id = $2
			FOR UPDATE`, orgID, *runtimeID).Scan(&runtimeStatus, &leaseExpiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			runtimeMissing = true
		} else if err != nil {
			return false, fmt.Errorf("lock activity phase runtime: %w", err)
		}
	}

	var lockedThreadID uuid.UUID
	var lockedRuntimeValue any
	var startedAt time.Time
	if err := tx.QueryRow(ctx, `
		SELECT thread_id, runtime_id, started_at
		FROM session_activity_phases
		WHERE org_id = $1 AND id = $2 AND status = 'running'
		FOR UPDATE`, orgID, phaseID).Scan(&lockedThreadID, &lockedRuntimeValue, &startedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("lock stranded activity phase: %w", err)
	}
	lockedRuntimeID, err := nullableUUID(lockedRuntimeValue)
	if err != nil {
		return false, fmt.Errorf("lock stranded activity phase: %w", err)
	}
	if lockedThreadID != threadID || !nullableUUIDEqual(lockedRuntimeID, runtimeID) || startedAt.After(completedAt) {
		return false, nil
	}

	stranded := false
	if runtimeID == nil {
		stranded = startedAt.Before(leaseExpiredBefore) &&
			threadStatus != models.ThreadStatusPending &&
			threadStatus != models.ThreadStatusRunning
	} else {
		stranded = runtimeMissing ||
			runtimeStatus == models.ThreadRuntimeStatusLost ||
			runtimeStatus == models.ThreadRuntimeStatusClosed ||
			runtimeStatus == models.ThreadRuntimeStatusFailed ||
			!leaseExpiresAt.Valid || leaseExpiresAt.Time.Before(leaseExpiredBefore)
	}
	if !stranded {
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit preserved activity phase: %w", err)
		}
		return false, nil
	}
	tag, err := tx.Exec(ctx, `
		UPDATE session_activity_phases
		SET status = 'interrupted', boundary_reason = 'runtime_lost',
			completed_at = $3, updated_at = now()
		WHERE org_id = $1 AND id = $2 AND status = 'running'`,
		orgID, phaseID, completedAt)
	if err != nil {
		return false, fmt.Errorf("reconcile stranded activity phase: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit reconciled activity phase: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ReconcileAbandonedInboxBatchesAcrossOrgs atomically abandons a bounded set of acknowledged batches after runtime loss.
// lint:allow-no-orgid reason="system reaper atomically abandons bounded acknowledged batches after runtime loss"
func (s *SessionActivityPhaseStore) ReconcileAbandonedInboxBatchesAcrossOrgs(ctx context.Context, leaseExpiredBefore, abandonedAt time.Time, limit int) ([]models.ThreadInboxDeliveryBatch, error) {
	if limit <= 0 || limit > 500 {
		return nil, fmt.Errorf("reconcile abandoned inbox delivery batches: limit must be between 1 and 500")
	}
	abandonedAt = postgresTimestamp(abandonedAt)
	rows, err := s.db.Query(ctx, `
		WITH candidates AS (
			SELECT b.id
			FROM thread_inbox_delivery_batches b
			JOIN thread_runtimes r ON r.org_id = b.org_id AND r.id = b.runtime_id
			WHERE b.status = 'acknowledged'
			  AND b.acknowledged_at < $1
			  AND (r.status IN ('lost', 'closed', 'failed')
			       OR r.lease_expires_at IS NULL OR r.lease_expires_at < $1)
			ORDER BY b.acknowledged_at, b.id
			LIMIT $2
			FOR UPDATE OF b, r SKIP LOCKED
		)
		UPDATE thread_inbox_delivery_batches b
		SET status = 'abandoned', abandoned_at = $3, updated_at = now()
		FROM candidates c
		WHERE b.id = c.id AND b.status = 'acknowledged'
		RETURNING `+inboxBatchColumnsB, leaseExpiredBefore, limit, abandonedAt)
	if err != nil {
		return nil, fmt.Errorf("reconcile abandoned inbox delivery batches: %w", err)
	}
	batches, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.ThreadInboxDeliveryBatch])
	if err != nil {
		return nil, fmt.Errorf("collect reconciled abandoned inbox delivery batches: %w", err)
	}
	return batches, nil
}

func (s *SessionActivityPhaseStore) AbandonInboxBatch(ctx context.Context, orgID, batchID uuid.UUID, abandonedAt time.Time) (models.ThreadInboxDeliveryBatch, error) {
	abandonedAt = postgresTimestamp(abandonedAt)
	rows, err := s.db.Query(ctx, `
		UPDATE thread_inbox_delivery_batches
		SET status = 'abandoned', abandoned_at = $3, updated_at = now()
		WHERE org_id = $1 AND id = $2 AND status = 'acknowledged'
		RETURNING `+inboxBatchColumns, orgID, batchID, abandonedAt)
	if err != nil {
		return models.ThreadInboxDeliveryBatch{}, fmt.Errorf("abandon inbox delivery batch: %w", err)
	}
	batch, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.ThreadInboxDeliveryBatch])
	if errors.Is(err, pgx.ErrNoRows) {
		return models.ThreadInboxDeliveryBatch{}, ErrInboxBatchConflict
	}
	if err != nil {
		return models.ThreadInboxDeliveryBatch{}, fmt.Errorf("collect abandoned inbox delivery batch: %w", err)
	}
	return batch, nil
}

func (s *SessionActivityPhaseStore) AcknowledgeInboxBatch(ctx context.Context, orgID, sessionID, threadID, runtimeID, leaseToken uuid.UUID, activePhaseID *uuid.UUID, sequenceEnd int64, acknowledgedAt time.Time) (models.ThreadInboxDeliveryBatch, error) {
	return s.acknowledgeInboxBatch(ctx, orgID, sessionID, threadID, runtimeID, leaseToken, activePhaseID, sequenceEnd, acknowledgedAt, nil)
}

func (s *SessionActivityPhaseStore) AcknowledgeInboxBatchWithTransition(ctx context.Context, orgID, sessionID, threadID, runtimeID, leaseToken uuid.UUID, activePhaseID *uuid.UUID, sequenceEnd int64, acknowledgedAt time.Time) (models.ThreadInboxDeliveryBatch, bool, error) {
	phaseTransitioned := false
	batch, err := s.acknowledgeInboxBatch(ctx, orgID, sessionID, threadID, runtimeID, leaseToken, activePhaseID, sequenceEnd, acknowledgedAt, &phaseTransitioned)
	return batch, phaseTransitioned, err
}

func (s *SessionActivityPhaseStore) acknowledgeInboxBatch(ctx context.Context, orgID, sessionID, threadID, runtimeID, leaseToken uuid.UUID, activePhaseID *uuid.UUID, sequenceEnd int64, acknowledgedAt time.Time, phaseTransitioned *bool) (models.ThreadInboxDeliveryBatch, error) {
	acknowledgedAt = postgresTimestamp(acknowledgedAt)
	if sequenceEnd <= 0 || acknowledgedAt.IsZero() {
		return models.ThreadInboxDeliveryBatch{}, fmt.Errorf("acknowledge inbox delivery batch: invalid sequence or acknowledgment time")
	}
	replayRows, err := s.db.Query(ctx, `SELECT `+inboxBatchColumns+`
		FROM thread_inbox_delivery_batches
		WHERE org_id = $1 AND session_id = $2 AND thread_id = $3
		  AND runtime_id = $4 AND sequence_end = $5`,
		orgID, sessionID, threadID, runtimeID, sequenceEnd)
	if err != nil {
		return models.ThreadInboxDeliveryBatch{}, fmt.Errorf("check replayed inbox delivery batch: %w", err)
	}
	replayed, replayErr := pgx.CollectOneRow(replayRows, pgx.RowToStructByName[models.ThreadInboxDeliveryBatch])
	if replayErr == nil {
		return replayed, nil
	}
	if !errors.Is(replayErr, pgx.ErrNoRows) {
		return models.ThreadInboxDeliveryBatch{}, fmt.Errorf("collect replayed inbox delivery batch: %w", replayErr)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return models.ThreadInboxDeliveryBatch{}, fmt.Errorf("begin acknowledge inbox delivery batch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var previous int64
	if err := tx.QueryRow(ctx, `
		SELECT last_acked_sequence
		FROM thread_runtimes
		WHERE org_id = $1 AND session_id = $2 AND thread_id = $3 AND id = $4
		  AND lease_token = $5 AND status IN ('starting', 'live', 'paused', 'draining')
		  AND lease_expires_at IS NOT NULL AND lease_expires_at > $6
		FOR UPDATE`, orgID, sessionID, threadID, runtimeID, leaseToken, acknowledgedAt).Scan(&previous); err != nil {
		return models.ThreadInboxDeliveryBatch{}, fmt.Errorf("lock thread runtime for inbox acknowledgment: %w", err)
	}
	sequenceStart := previous + 1
	if sequenceEnd < previous {
		return models.ThreadInboxDeliveryBatch{}, ErrInboxBatchConflict
	}
	if sequenceEnd == previous {
		rows, queryErr := tx.Query(ctx, `SELECT `+inboxBatchColumns+`
			FROM thread_inbox_delivery_batches
			WHERE org_id = $1 AND session_id = $2 AND thread_id = $3
			  AND runtime_id = $4 AND sequence_end = $5`,
			orgID, sessionID, threadID, runtimeID, sequenceEnd)
		if queryErr != nil {
			return models.ThreadInboxDeliveryBatch{}, fmt.Errorf("get concurrently replayed inbox delivery batch: %w", queryErr)
		}
		batch, queryErr := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.ThreadInboxDeliveryBatch])
		if queryErr != nil {
			return models.ThreadInboxDeliveryBatch{}, ErrInboxBatchConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return models.ThreadInboxDeliveryBatch{}, fmt.Errorf("commit concurrently replayed inbox delivery batch: %w", err)
		}
		return batch, nil
	}

	var matching int64
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM thread_inbox_entries
		WHERE org_id = $1 AND session_id = $2 AND thread_id = $3
		  AND runtime_id = $4 AND sequence_no BETWEEN $5 AND $6
		  AND delivery_state = 'delivered'`,
		orgID, sessionID, threadID, runtimeID, sequenceStart, sequenceEnd).Scan(&matching); err != nil {
		return models.ThreadInboxDeliveryBatch{}, fmt.Errorf("validate inbox delivery batch entries: %w", err)
	}
	if matching != sequenceEnd-sequenceStart+1 {
		return models.ThreadInboxDeliveryBatch{}, ErrInboxBatchConflict
	}
	if activePhaseID != nil {
		tag, updateErr := tx.Exec(ctx, `
			UPDATE session_activity_phases
			SET status = 'completed', boundary_reason = 'steered',
				completed_at = $3, updated_at = now()
			WHERE org_id = $1 AND id = $2 AND session_id = $4 AND thread_id = $5
			  AND runtime_id = $6 AND status = 'running' AND started_at <= $3`,
			orgID, *activePhaseID, acknowledgedAt, sessionID, threadID, runtimeID)
		if updateErr != nil {
			return models.ThreadInboxDeliveryBatch{}, fmt.Errorf("close steered activity phase: %w", updateErr)
		}
		if tag.RowsAffected() != 1 {
			return models.ThreadInboxDeliveryBatch{}, ErrActivityPhaseConflict
		}
		if phaseTransitioned != nil {
			*phaseTransitioned = true
		}
	}

	rows, err := tx.Query(ctx, `
		INSERT INTO thread_inbox_delivery_batches (
			org_id, session_id, thread_id, runtime_id, sequence_start, sequence_end,
			status, acknowledged_at
		) VALUES ($1, $2, $3, $4, $5, $6, 'acknowledged', $7)
		RETURNING `+inboxBatchColumns,
		orgID, sessionID, threadID, runtimeID, sequenceStart, sequenceEnd, acknowledgedAt)
	if err != nil {
		return models.ThreadInboxDeliveryBatch{}, fmt.Errorf("insert inbox delivery batch: %w", err)
	}
	batch, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.ThreadInboxDeliveryBatch])
	if err != nil {
		return models.ThreadInboxDeliveryBatch{}, fmt.Errorf("collect acknowledged inbox delivery batch: %w", err)
	}
	ackedTag, err := tx.Exec(ctx, `
		UPDATE thread_inbox_entries
		SET delivery_state = 'acked', acked_at = COALESCE(acked_at, $7), updated_at = now()
		WHERE org_id = $1 AND session_id = $2 AND thread_id = $3 AND runtime_id = $4
		  AND sequence_no BETWEEN $5 AND $6 AND delivery_state = 'delivered'`,
		orgID, sessionID, threadID, runtimeID, sequenceStart, sequenceEnd, acknowledgedAt)
	if err != nil {
		return models.ThreadInboxDeliveryBatch{}, fmt.Errorf("mark inbox delivery batch entries acknowledged: %w", err)
	}
	if ackedTag.RowsAffected() != matching {
		return models.ThreadInboxDeliveryBatch{}, ErrInboxBatchConflict
	}
	tag, err := tx.Exec(ctx, `
		UPDATE thread_runtimes
		SET last_acked_sequence = $6, heartbeat_at = $7, updated_at = now()
		WHERE org_id = $1 AND session_id = $2 AND thread_id = $3 AND id = $4
		  AND lease_token = $5 AND last_acked_sequence = $8`,
		orgID, sessionID, threadID, runtimeID, leaseToken, sequenceEnd, acknowledgedAt, previous)
	if err != nil {
		return models.ThreadInboxDeliveryBatch{}, fmt.Errorf("advance inbox acknowledgment watermark: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return models.ThreadInboxDeliveryBatch{}, ErrInboxBatchConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return models.ThreadInboxDeliveryBatch{}, fmt.Errorf("commit acknowledged inbox delivery batch: %w", err)
	}
	return batch, nil
}

func validateTerminalActivityPhase(status models.ActivityPhaseStatus, reason models.ActivityPhaseBoundaryReason, completedAt time.Time) error {
	if err := status.Validate(); err != nil {
		return err
	}
	if err := reason.Validate(); err != nil {
		return err
	}
	if completedAt.IsZero() || status == models.ActivityPhaseStatusRunning {
		return fmt.Errorf("terminal activity phase requires a terminal status and completion time")
	}
	valid := (status == models.ActivityPhaseStatusCompleted &&
		(reason == models.ActivityPhaseBoundaryFinalResponse || reason == models.ActivityPhaseBoundaryHumanInput ||
			reason == models.ActivityPhaseBoundaryApproval || reason == models.ActivityPhaseBoundaryPlanApproval ||
			reason == models.ActivityPhaseBoundarySteered)) ||
		(status == models.ActivityPhaseStatusFailed && reason == models.ActivityPhaseBoundaryError) ||
		(status == models.ActivityPhaseStatusCancelled &&
			(reason == models.ActivityPhaseBoundaryStopped || reason == models.ActivityPhaseBoundaryCancelled)) ||
		(status == models.ActivityPhaseStatusInterrupted &&
			(reason == models.ActivityPhaseBoundaryMaintenance || reason == models.ActivityPhaseBoundaryRuntimeLost ||
				reason == models.ActivityPhaseBoundaryCapacitySuspended || reason == models.ActivityPhaseBoundaryInterrupted))
	if !valid {
		return fmt.Errorf("invalid activity phase status/reason combination: %s/%s", status, reason)
	}
	return nil
}

func postgresTimestamp(value time.Time) time.Time {
	return value.Truncate(time.Microsecond)
}

func nullableUUID(value any) (*uuid.UUID, error) {
	switch value := value.(type) {
	case nil:
		return nil, nil
	case uuid.UUID:
		return &value, nil
	case [16]byte:
		id := uuid.UUID(value)
		return &id, nil
	case pgtype.UUID:
		if !value.Valid {
			return nil, nil
		}
		id := uuid.UUID(value.Bytes)
		return &id, nil
	default:
		return nil, fmt.Errorf("unsupported UUID type %T", value)
	}
}

func nullableUUIDEqual(first, second *uuid.UUID) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	return *first == *second
}
