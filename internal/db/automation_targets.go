package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	// ErrAutomationTargetNotFound is returned when a target row does not exist
	// in the org, or when LockOrCreate could not create one because the
	// automation or repository is not in the org.
	ErrAutomationTargetNotFound = errors.New("automation target not found")
	// ErrAutomationTargetGenerationNotFound is returned when no generation row
	// matches the lookup (including "no active generation").
	ErrAutomationTargetGenerationNotFound = errors.New("automation target generation not found")
	// ErrAutomationTargetGenerationNotActive is returned when a mutation that
	// requires an active generation finds it already retired.
	ErrAutomationTargetGenerationNotActive = errors.New("automation target generation is not active")
	// ErrAutomationTargetSessionMismatch is returned when a generation insert
	// references a session outside the target's org.
	ErrAutomationTargetSessionMismatch = errors.New("session does not belong to the automation target's organization")
)

// AutomationTargetStore persists automation_targets (the identity, lifecycle,
// lock, and wake-outbox row per automation and pull request) and
// automation_target_sessions (one row per generation of the target's
// session). See design doc 125.
//
// Methods that take a pgx.Tx participate in the caller's target-locked
// transaction; the target row is the only lock needed between turns because
// an owned session accepts no human turns.
type AutomationTargetStore struct {
	db TxStarter
}

func NewAutomationTargetStore(db TxStarter) *AutomationTargetStore {
	return &AutomationTargetStore{db: db}
}

const automationTargetColumns = `id, org_id, automation_id, repository_id, target_kind, target_key,
	active_generation, lifecycle_state, lifecycle_updated_at, observed_head_sha, observed_head_updated_at,
	head_epoch, head_resolution_pending, head_resolution_deadline_at, wake_requested_at, created_at, updated_at`

// AutomationTargetColumnNames is automationTargetColumns as a slice, exported
// for pgxmock rows in other packages' tests.
var AutomationTargetColumnNames = []string{
	"id", "org_id", "automation_id", "repository_id", "target_kind", "target_key",
	"active_generation", "lifecycle_state", "lifecycle_updated_at", "observed_head_sha", "observed_head_updated_at",
	"head_epoch", "head_resolution_pending", "head_resolution_deadline_at", "wake_requested_at", "created_at", "updated_at",
}

// AutomationTargetSessionColumnNames is automationTargetSessionColumns as a
// slice, exported for pgxmock rows in other packages' tests.
var AutomationTargetSessionColumnNames = []string{
	"id", "org_id", "target_id", "generation", "session_id", "status", "retired_reason", "retired_at",
	"turn_count", "ownership_release_pending", "last_attempted_head_sha", "last_reviewed_head_sha", "last_reviewed_epoch",
	"checkpoint_snapshot_key", "checkpoint_head_sha", "checkpoint_dependency_fingerprint", "checkpoint_review_complete",
	"last_base_ref", "last_run_id", "last_turn_at", "created_at", "updated_at",
}

// AutomationTargetSessionRow renders a generation as pgxmock row values in
// AutomationTargetSessionColumnNames order.
func AutomationTargetSessionRow(g models.AutomationTargetSession) []any {
	return []any{
		g.ID, g.OrgID, g.TargetID, g.Generation, g.SessionID, g.Status, g.RetiredReason, g.RetiredAt,
		g.TurnCount, g.OwnershipReleasePending, g.LastAttemptedHeadSHA, g.LastReviewedHeadSHA, g.LastReviewedEpoch,
		g.CheckpointSnapshotKey, g.CheckpointHeadSHA, g.CheckpointDependencyFingerprint, g.CheckpointReviewComplete,
		g.LastBaseRef, g.LastRunID, g.LastTurnAt, g.CreatedAt, g.UpdatedAt,
	}
}

// AutomationTargetRow renders a target as pgxmock row values in
// AutomationTargetColumnNames order.
func AutomationTargetRow(t models.AutomationTarget) []any {
	return []any{
		t.ID, t.OrgID, t.AutomationID, t.RepositoryID, t.TargetKind, t.TargetKey,
		t.ActiveGeneration, t.LifecycleState, t.LifecycleUpdatedAt, t.ObservedHeadSHA, t.ObservedHeadUpdatedAt,
		t.HeadEpoch, t.HeadResolutionPending, t.HeadResolutionDeadlineAt, t.WakeRequestedAt, t.CreatedAt, t.UpdatedAt,
	}
}

func scanAutomationTarget(row pgx.Row) (models.AutomationTarget, error) {
	var t models.AutomationTarget
	err := row.Scan(
		&t.ID, &t.OrgID, &t.AutomationID, &t.RepositoryID, &t.TargetKind, &t.TargetKey,
		&t.ActiveGeneration, &t.LifecycleState, &t.LifecycleUpdatedAt, &t.ObservedHeadSHA, &t.ObservedHeadUpdatedAt,
		&t.HeadEpoch, &t.HeadResolutionPending, &t.HeadResolutionDeadlineAt, &t.WakeRequestedAt, &t.CreatedAt, &t.UpdatedAt,
	)
	return t, err
}

const automationTargetSessionColumns = `id, org_id, target_id, generation, session_id, status, retired_reason, retired_at,
	turn_count, ownership_release_pending, last_attempted_head_sha, last_reviewed_head_sha, last_reviewed_epoch,
	checkpoint_snapshot_key, checkpoint_head_sha, checkpoint_dependency_fingerprint, checkpoint_review_complete,
	last_base_ref, last_run_id, last_turn_at, created_at, updated_at`

func scanAutomationTargetSession(row pgx.Row) (models.AutomationTargetSession, error) {
	var g models.AutomationTargetSession
	err := row.Scan(
		&g.ID, &g.OrgID, &g.TargetID, &g.Generation, &g.SessionID, &g.Status, &g.RetiredReason, &g.RetiredAt,
		&g.TurnCount, &g.OwnershipReleasePending, &g.LastAttemptedHeadSHA, &g.LastReviewedHeadSHA, &g.LastReviewedEpoch,
		&g.CheckpointSnapshotKey, &g.CheckpointHeadSHA, &g.CheckpointDependencyFingerprint, &g.CheckpointReviewComplete,
		&g.LastBaseRef, &g.LastRunID, &g.LastTurnAt, &g.CreatedAt, &g.UpdatedAt,
	)
	return g, err
}

// lockAutomationTargets takes the transaction-scoped advisory lock that
// serializes target creation with automation-wide target operations
// (disabling continuity). Row locks alone cannot do this: a target row that
// another transaction has inserted but not committed is invisible to a
// FOR UPDATE scan. Every path that creates a target or acts on all of an
// automation's targets takes this lock first, then target row locks, so
// the lock order is fixed.
func lockAutomationTargets(ctx context.Context, tx pgx.Tx, orgID, automationID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('automation_targets'), hashtext(@key))`,
		pgx.NamedArgs{"key": orgID.String() + ":" + automationID.String()}); err != nil {
		return fmt.Errorf("lock automation targets: %w", err)
	}
	return nil
}

// LockOrCreate upserts the target row for (automation, repository, kind, key)
// and returns it locked FOR UPDATE for the rest of tx. It first takes the
// automation-scoped advisory lock (see lockAutomationTargets), so a
// continuity switch in flight for the automation completes before a new
// target can be created, and the caller's later read of the automation row
// observes the switched mode. The insert validates that the automation and
// repository belong to orgID, because UUID foreign keys alone do not
// enforce same-org relationships; a cross-org reference surfaces as
// ErrAutomationTargetNotFound.
func (s *AutomationTargetStore) LockOrCreate(ctx context.Context, tx pgx.Tx, orgID, automationID, repositoryID uuid.UUID, kind models.AutomationTargetKind, key string) (models.AutomationTarget, error) {
	if err := kind.Validate(); err != nil {
		return models.AutomationTarget{}, err
	}
	if len(key) == 0 || len(key) > 64 {
		return models.AutomationTarget{}, fmt.Errorf("automation target key must be 1 to 64 characters")
	}
	if err := lockAutomationTargets(ctx, tx, orgID, automationID); err != nil {
		return models.AutomationTarget{}, err
	}
	args := pgx.NamedArgs{
		"org_id":        orgID,
		"automation_id": automationID,
		"repository_id": repositoryID,
		"target_kind":   kind,
		"target_key":    key,
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO automation_targets (org_id, automation_id, repository_id, target_kind, target_key)
		SELECT @org_id, @automation_id, @repository_id, @target_kind, @target_key
		WHERE EXISTS (
			SELECT 1 FROM automations
			WHERE id = @automation_id AND org_id = @org_id AND deleted_at IS NULL)
		  AND EXISTS (
			SELECT 1 FROM repositories
			WHERE id = @repository_id AND org_id = @org_id)
		ON CONFLICT (org_id, automation_id, repository_id, target_kind, target_key) DO NOTHING`, args); err != nil {
		return models.AutomationTarget{}, fmt.Errorf("upsert automation target: %w", err)
	}
	row := tx.QueryRow(ctx, `SELECT `+automationTargetColumns+`
		FROM automation_targets
		WHERE org_id = @org_id AND automation_id = @automation_id AND repository_id = @repository_id
		  AND target_kind = @target_kind AND target_key = @target_key
		FOR UPDATE`, args)
	target, err := scanAutomationTarget(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.AutomationTarget{}, ErrAutomationTargetNotFound
	}
	if err != nil {
		return models.AutomationTarget{}, fmt.Errorf("lock automation target: %w", err)
	}
	return target, nil
}

// LockByID locks an existing target row FOR UPDATE for the rest of tx.
func (s *AutomationTargetStore) LockByID(ctx context.Context, tx pgx.Tx, orgID, targetID uuid.UUID) (models.AutomationTarget, error) {
	row := tx.QueryRow(ctx, `SELECT `+automationTargetColumns+`
		FROM automation_targets
		WHERE id = @id AND org_id = @org_id
		FOR UPDATE`, pgx.NamedArgs{"id": targetID, "org_id": orgID})
	target, err := scanAutomationTarget(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.AutomationTarget{}, ErrAutomationTargetNotFound
	}
	if err != nil {
		return models.AutomationTarget{}, fmt.Errorf("lock automation target: %w", err)
	}
	return target, nil
}

func (s *AutomationTargetStore) GetByID(ctx context.Context, orgID, targetID uuid.UUID) (models.AutomationTarget, error) {
	row := s.db.QueryRow(ctx, `SELECT `+automationTargetColumns+`
		FROM automation_targets
		WHERE id = @id AND org_id = @org_id`, pgx.NamedArgs{"id": targetID, "org_id": orgID})
	target, err := scanAutomationTarget(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.AutomationTarget{}, ErrAutomationTargetNotFound
	}
	if err != nil {
		return models.AutomationTarget{}, fmt.Errorf("get automation target: %w", err)
	}
	return target, nil
}

// GetActiveGeneration returns the target's active generation row, or
// ErrAutomationTargetGenerationNotFound when none is active. q may be a
// transaction holding the target lock or the plain pool.
func (s *AutomationTargetStore) GetActiveGeneration(ctx context.Context, q DBTX, orgID, targetID uuid.UUID) (models.AutomationTargetSession, error) {
	if q == nil {
		q = s.db
	}
	row := q.QueryRow(ctx, `SELECT `+automationTargetSessionColumns+`
		FROM automation_target_sessions
		WHERE org_id = @org_id AND target_id = @target_id AND status = 'active'`,
		pgx.NamedArgs{"org_id": orgID, "target_id": targetID})
	generation, err := scanAutomationTargetSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.AutomationTargetSession{}, ErrAutomationTargetGenerationNotFound
	}
	if err != nil {
		return models.AutomationTargetSession{}, fmt.Errorf("get active automation target generation: %w", err)
	}
	return generation, nil
}

func (s *AutomationTargetStore) GetGenerationByID(ctx context.Context, orgID, generationID uuid.UUID) (models.AutomationTargetSession, error) {
	row := s.db.QueryRow(ctx, `SELECT `+automationTargetSessionColumns+`
		FROM automation_target_sessions
		WHERE id = @id AND org_id = @org_id`, pgx.NamedArgs{"id": generationID, "org_id": orgID})
	generation, err := scanAutomationTargetSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.AutomationTargetSession{}, ErrAutomationTargetGenerationNotFound
	}
	if err != nil {
		return models.AutomationTargetSession{}, fmt.Errorf("get automation target generation: %w", err)
	}
	return generation, nil
}

// GetActiveGenerationBySession returns the active generation that owns
// sessionID, or ErrAutomationTargetGenerationNotFound when the session is
// not an active generation of any target.
func (s *AutomationTargetStore) GetActiveGenerationBySession(ctx context.Context, orgID, sessionID uuid.UUID) (models.AutomationTargetSession, error) {
	row := s.db.QueryRow(ctx, `SELECT `+automationTargetSessionColumns+`
		FROM automation_target_sessions
		WHERE org_id = @org_id AND session_id = @session_id AND status = 'active'`,
		pgx.NamedArgs{"org_id": orgID, "session_id": sessionID})
	generation, err := scanAutomationTargetSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.AutomationTargetSession{}, ErrAutomationTargetGenerationNotFound
	}
	if err != nil {
		return models.AutomationTargetSession{}, fmt.Errorf("get automation target generation by session: %w", err)
	}
	return generation, nil
}

// InsertGeneration records sessionID as the next generation of the target,
// advances automation_targets.active_generation, and marks the session
// automation-owned, all inside the caller's target-locked tx. The partial
// unique index on active generations rejects a second active row, so the
// caller must retire the previous generation first. The session must belong
// to orgID.
func (s *AutomationTargetStore) InsertGeneration(ctx context.Context, tx pgx.Tx, orgID, targetID, sessionID uuid.UUID) (models.AutomationTargetSession, error) {
	var generation int
	err := tx.QueryRow(ctx, `
		UPDATE automation_targets
		SET active_generation = active_generation + 1, updated_at = now()
		WHERE id = @target_id AND org_id = @org_id
		RETURNING active_generation`,
		pgx.NamedArgs{"target_id": targetID, "org_id": orgID}).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.AutomationTargetSession{}, ErrAutomationTargetNotFound
	}
	if err != nil {
		return models.AutomationTargetSession{}, fmt.Errorf("advance automation target generation: %w", err)
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO automation_target_sessions (org_id, target_id, generation, session_id)
		SELECT @org_id, @target_id, @generation, @session_id
		WHERE EXISTS (SELECT 1 FROM sessions WHERE id = @session_id AND org_id = @org_id)
		RETURNING `+automationTargetSessionColumns,
		pgx.NamedArgs{"org_id": orgID, "target_id": targetID, "generation": generation, "session_id": sessionID})
	inserted, err := scanAutomationTargetSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.AutomationTargetSession{}, ErrAutomationTargetSessionMismatch
	}
	if err != nil {
		return models.AutomationTargetSession{}, fmt.Errorf("insert automation target generation: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE sessions
		SET automation_owner_generation_id = @generation_id
		WHERE id = @session_id AND org_id = @org_id`,
		pgx.NamedArgs{"generation_id": inserted.ID, "session_id": sessionID, "org_id": orgID}); err != nil {
		return models.AutomationTargetSession{}, fmt.Errorf("mark session automation-owned: %w", err)
	}
	return inserted, nil
}

// RetireGeneration retires an active generation and releases session
// ownership now or marks it pending, per design doc 125 "Session Ownership":
// when no run is executing for the generation and the session holds no turn
// hold, the owner marker is cleared in the same transaction; otherwise
// ownership_release_pending is set and the executing run's completion clears
// the marker. It takes the target lock, writes the wake outbox, and returns
// the retired row. Returns ErrAutomationTargetGenerationNotActive when the
// generation is already retired.
func (s *AutomationTargetStore) RetireGeneration(ctx context.Context, tx pgx.Tx, orgID, generationID uuid.UUID, reason models.AutomationTargetRetiredReason) (models.AutomationTargetSession, error) {
	if err := reason.Validate(); err != nil {
		return models.AutomationTargetSession{}, err
	}
	var targetID uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT t.id
		FROM automation_targets t
		JOIN automation_target_sessions g ON g.target_id = t.id AND g.org_id = t.org_id
		WHERE g.id = @generation_id AND g.org_id = @org_id
		FOR UPDATE OF t`,
		pgx.NamedArgs{"generation_id": generationID, "org_id": orgID}).Scan(&targetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.AutomationTargetSession{}, ErrAutomationTargetGenerationNotFound
	}
	if err != nil {
		return models.AutomationTargetSession{}, fmt.Errorf("lock automation target for retirement: %w", err)
	}

	row := tx.QueryRow(ctx, `
		UPDATE automation_target_sessions g
		SET status = 'retired',
			retired_reason = @reason,
			retired_at = now(),
			updated_at = now(),
			ownership_release_pending = EXISTS (
				SELECT 1 FROM automation_runs r
				WHERE r.org_id = g.org_id AND r.target_id = g.target_id
				  AND r.target_generation = g.generation AND r.dispatch_state = 'executing')
			OR EXISTS (
				SELECT 1 FROM sessions s
				WHERE s.org_id = g.org_id AND s.id = g.session_id AND s.turn_holding_container)
		WHERE g.id = @generation_id AND g.org_id = @org_id AND g.status = 'active'
		RETURNING `+automationTargetSessionColumns,
		pgx.NamedArgs{"generation_id": generationID, "org_id": orgID, "reason": reason})
	retired, err := scanAutomationTargetSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.AutomationTargetSession{}, ErrAutomationTargetGenerationNotActive
	}
	if err != nil {
		return models.AutomationTargetSession{}, fmt.Errorf("retire automation target generation: %w", err)
	}

	if !retired.OwnershipReleasePending {
		if _, err := tx.Exec(ctx, `
			UPDATE sessions
			SET automation_owner_generation_id = NULL
			WHERE id = @session_id AND org_id = @org_id AND automation_owner_generation_id = @generation_id`,
			pgx.NamedArgs{"session_id": retired.SessionID, "org_id": orgID, "generation_id": generationID}); err != nil {
			return models.AutomationTargetSession{}, fmt.Errorf("release session automation ownership: %w", err)
		}
	}
	if _, err := s.RequestWake(ctx, tx, orgID, targetID); err != nil {
		return models.AutomationTargetSession{}, err
	}
	return retired, nil
}

// RetireActiveGenerationsForAutomation retires every active generation of
// the automation's targets with reason, applying RetireGeneration's
// ownership-release rules to each. Used when continuity is switched back to
// per_run. Returns the retired rows.
//
// The automation-scoped advisory lock is taken first, so a target that a
// concurrent ownership transaction is still inserting (invisible to a row
// scan) cannot slip past the switch: that transaction holds the same lock
// from LockOrCreate until it commits. Then every target row of the
// automation is locked in a stable order, and each target's active
// generation is read only after its lock is held; reading generations
// before locking, or filtering targets by a committed active_generation,
// would skip a replacement or a first generation committed in between.
func (s *AutomationTargetStore) RetireActiveGenerationsForAutomation(ctx context.Context, tx pgx.Tx, orgID, automationID uuid.UUID, reason models.AutomationTargetRetiredReason) ([]models.AutomationTargetSession, error) {
	if err := reason.Validate(); err != nil {
		return nil, err
	}
	if err := lockAutomationTargets(ctx, tx, orgID, automationID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT id
		FROM automation_targets
		WHERE org_id = @org_id AND automation_id = @automation_id
		ORDER BY id
		FOR UPDATE`,
		pgx.NamedArgs{"org_id": orgID, "automation_id": automationID})
	if err != nil {
		return nil, fmt.Errorf("lock automation targets: %w", err)
	}
	targetIDs, err := collectIDs(rows)
	if err != nil {
		return nil, fmt.Errorf("scan automation targets: %w", err)
	}
	retired := make([]models.AutomationTargetSession, 0, len(targetIDs))
	for _, targetID := range targetIDs {
		active, err := s.GetActiveGeneration(ctx, tx, orgID, targetID)
		if errors.Is(err, ErrAutomationTargetGenerationNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		generation, err := s.RetireGeneration(ctx, tx, orgID, active.ID, reason)
		if err != nil {
			return nil, err
		}
		retired = append(retired, generation)
	}
	return retired, nil
}

// SetLifecycle records the pull request's lifecycle state on the target.
func (s *AutomationTargetStore) SetLifecycle(ctx context.Context, q DBTX, orgID, targetID uuid.UUID, state models.AutomationTargetLifecycleState) error {
	if err := state.Validate(); err != nil {
		return err
	}
	if q == nil {
		q = s.db
	}
	tag, err := q.Exec(ctx, `
		UPDATE automation_targets
		SET lifecycle_state = @state, lifecycle_updated_at = now(), updated_at = now()
		WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": targetID, "org_id": orgID, "state": state})
	if err != nil {
		return fmt.Errorf("set automation target lifecycle: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAutomationTargetNotFound
	}
	return nil
}

// SetLifecycleObserved records a lifecycle state observed at observedAt,
// the source's own timestamp (a webhook's pull_request.updated_at) rather
// than the time the delivery was processed, so a delayed delivery cannot
// pass for fresh openness evidence. A transition takes the observation
// time as is; a same-state refresh only ever moves the evidence forward.
func (s *AutomationTargetStore) SetLifecycleObserved(ctx context.Context, q DBTX, orgID, targetID uuid.UUID, state models.AutomationTargetLifecycleState, observedAt time.Time) error {
	if err := state.Validate(); err != nil {
		return err
	}
	if q == nil {
		q = s.db
	}
	tag, err := q.Exec(ctx, `
		UPDATE automation_targets
		SET lifecycle_updated_at = CASE
		        WHEN lifecycle_state = @state THEN GREATEST(lifecycle_updated_at, @observed_at::timestamptz)
		        ELSE @observed_at::timestamptz
		    END,
		    lifecycle_state = @state,
		    updated_at = now()
		WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": targetID, "org_id": orgID, "state": state, "observed_at": observedAt})
	if err != nil {
		return fmt.Errorf("set automation target lifecycle: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAutomationTargetNotFound
	}
	return nil
}

// RequestWake writes the wake outbox marker and returns the recorded time.
// Callers enqueue the automation_target_wake job in the same transaction;
// ClearWake later clears the marker only if no newer request arrived.
func (s *AutomationTargetStore) RequestWake(ctx context.Context, q DBTX, orgID, targetID uuid.UUID) (time.Time, error) {
	if q == nil {
		q = s.db
	}
	var requestedAt time.Time
	err := q.QueryRow(ctx, `
		UPDATE automation_targets
		SET wake_requested_at = now(), updated_at = now()
		WHERE id = @id AND org_id = @org_id
		RETURNING wake_requested_at`,
		pgx.NamedArgs{"id": targetID, "org_id": orgID}).Scan(&requestedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrAutomationTargetNotFound
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("request automation target wake: %w", err)
	}
	return requestedAt, nil
}

// ClearWake clears the wake outbox marker only when it still equals the
// value the caller observed (requestedAt), so a request written after the
// observation survives. Equality rather than ordering is required because
// now() is transaction-start time: a transaction that started earlier can
// write a later request carrying an older timestamp. Returns whether the
// marker was cleared.
func (s *AutomationTargetStore) ClearWake(ctx context.Context, q DBTX, orgID, targetID uuid.UUID, requestedAt time.Time) (bool, error) {
	if q == nil {
		q = s.db
	}
	tag, err := q.Exec(ctx, `
		UPDATE automation_targets
		SET wake_requested_at = NULL, updated_at = now()
		WHERE id = @id AND org_id = @org_id
		  AND wake_requested_at = @requested_at`,
		pgx.NamedArgs{"id": targetID, "org_id": orgID, "requested_at": requestedAt})
	if err != nil {
		return false, fmt.Errorf("clear automation target wake: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
