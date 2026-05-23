package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type SessionThreadStore struct {
	db DBTX
}

func NewSessionThreadStore(db DBTX) *SessionThreadStore {
	return &SessionThreadStore{db: db}
}

const sessionThreadSelectColumns = `id, session_id, org_id, agent_type, model_override,
	label, instructions, file_scope, status, agent_session_id, current_turn, last_activity_at,
	confidence_score, result_summary, diff, failure_explanation, failure_category,
	started_at, completed_at, created_at, archived_at,
	base_snapshot_key, cost_cents, pending_message_count, cancel_requested_at`

// sessionThreadListColumns omits the raw diff while preserving a lightweight
// truthy marker for UI affordances such as "Revert tab". Server-side actions
// that need the real patch use GetByID and sessionThreadSelectColumns.
const sessionThreadListColumns = `id, session_id, org_id, agent_type, model_override,
	label, instructions, file_scope, status, agent_session_id, current_turn, last_activity_at,
	confidence_score, result_summary,
	CASE WHEN diff IS NULL THEN NULL ELSE '__diff_present__' END AS diff,
	failure_explanation, failure_category,
	started_at, completed_at, created_at, archived_at,
	base_snapshot_key, cost_cents, pending_message_count, cancel_requested_at`

// ErrThreadLimitReached is returned when the maximum number of threads per session
// has been reached and a new thread cannot be created.
var ErrThreadLimitReached = fmt.Errorf("thread limit reached")

func (s *SessionThreadStore) Create(ctx context.Context, thread *models.SessionThread, maxThreads int) error {
	query := `
		INSERT INTO session_threads (
			session_id, org_id, agent_type, model_override,
			label, instructions, file_scope, status
		)
		SELECT @session_id, @org_id, @agent_type, @model_override,
			@label, @instructions, @file_scope, @status
		WHERE (SELECT count(*) FROM session_threads WHERE session_id = @session_id AND org_id = @org_id AND archived_at IS NULL) < @max_threads
		RETURNING id, created_at`

	args := pgx.NamedArgs{
		"session_id":     thread.SessionID,
		"org_id":         thread.OrgID,
		"agent_type":     thread.AgentType,
		"model_override": thread.ModelOverride,
		"label":          thread.Label,
		"instructions":   thread.Instructions,
		"file_scope":     thread.FileScope,
		"status":         thread.Status,
		"max_threads":    maxThreads,
	}

	row := s.db.QueryRow(ctx, query, args)
	err := row.Scan(&thread.ID, &thread.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrThreadLimitReached
		}
		return err
	}
	return nil
}

func (s *SessionThreadStore) GetByID(ctx context.Context, orgID, threadID uuid.UUID) (models.SessionThread, error) {
	query := `
		SELECT ` + sessionThreadSelectColumns + `
		FROM session_threads
		WHERE id = @id AND org_id = @org_id AND archived_at IS NULL`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     threadID,
		"org_id": orgID,
	})
	if err != nil {
		return models.SessionThread{}, fmt.Errorf("query session thread: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionThread])
}

func (s *SessionThreadStore) ListBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionThread, error) {
	query := `
		SELECT ` + sessionThreadListColumns + `
		FROM session_threads
		WHERE org_id = @org_id AND session_id = @session_id AND archived_at IS NULL
		ORDER BY created_at ASC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":     orgID,
		"session_id": sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("query session threads: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionThread])
}

func (s *SessionThreadStore) CountBySession(ctx context.Context, orgID, sessionID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM session_threads WHERE org_id = @org_id AND session_id = @session_id AND archived_at IS NULL`,
		pgx.NamedArgs{"org_id": orgID, "session_id": sessionID},
	).Scan(&count)
	return count, err
}

func (s *SessionThreadStore) Archive(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error) {
	query := `
		WITH visible_threads AS (
			SELECT id, status
			FROM session_threads
			WHERE session_id = @session_id
			  AND org_id = @org_id
			  AND archived_at IS NULL
			FOR UPDATE
		), visible_count AS (
			SELECT count(*) AS count
			FROM visible_threads
		)
		UPDATE session_threads
		SET archived_at = now()
		WHERE id = @id
		  AND session_id = @session_id
		  AND org_id = @org_id
		  AND archived_at IS NULL
		  AND (SELECT count FROM visible_count) > 1
		  AND (SELECT status FROM visible_threads WHERE id = @id) NOT IN ('pending', 'running', 'awaiting_input')
		RETURNING ` + sessionThreadSelectColumns

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":         threadID,
		"session_id": sessionID,
		"org_id":     orgID,
	})
	if err != nil {
		return models.SessionThread{}, fmt.Errorf("archive thread: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionThread])
}

func (s *SessionThreadStore) UpdateStatus(ctx context.Context, orgID, threadID uuid.UUID, status models.ThreadStatus) error {
	query := `UPDATE session_threads SET status = @status WHERE id = @id AND org_id = @org_id`
	if status == models.ThreadStatusRunning {
		query = `UPDATE session_threads SET status = @status, started_at = now(), cancel_requested_at = NULL WHERE id = @id AND org_id = @org_id`
	} else if status == models.ThreadStatusCompleted || status == models.ThreadStatusFailed || status == models.ThreadStatusCancelled {
		query = `UPDATE session_threads SET status = @status, completed_at = now() WHERE id = @id AND org_id = @org_id`
	}
	ct, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     threadID,
		"org_id": orgID,
		"status": status,
	})
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("thread not found")
	}
	return nil
}

func (s *SessionThreadStore) UpdateEditable(ctx context.Context, thread *models.SessionThread) error {
	query := `
		UPDATE session_threads
		SET agent_type = @agent_type,
		    model_override = @model_override,
		    label = @label
		WHERE id = @id
		  AND org_id = @org_id
		  AND session_id = @session_id
		  AND status = 'idle'
		  AND current_turn = 0
		RETURNING ` + sessionThreadSelectColumns

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":             thread.ID,
		"org_id":         thread.OrgID,
		"session_id":     thread.SessionID,
		"agent_type":     thread.AgentType,
		"model_override": thread.ModelOverride,
		"label":          thread.Label,
	})
	if err != nil {
		return fmt.Errorf("update editable thread fields: %w", err)
	}

	updated, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionThread])
	if err != nil {
		return err
	}
	*thread = updated
	return nil
}

// CompleteTurn marks the thread idle and advances its current_turn. It is the
// Phase 1 success path: the shared session UpdateTurnComplete already records
// confidence_score, result_summary, and diff at the session level, so this
// method intentionally does not duplicate them on the thread row. Phase 2
// (thread-scoped runtime execution) will need to populate the thread's own
// result columns — switch to UpdateTurnComplete on the thread store at that
// point so per-thread review surfaces have data to show.
func (s *SessionThreadStore) CompleteTurn(ctx context.Context, orgID, threadID uuid.UUID, turn int, agentSessionID string) error {
	query := `
		UPDATE session_threads
		SET status = 'idle',
		    current_turn = @current_turn,
		    last_activity_at = now(),
		    agent_session_id = COALESCE(@agent_session_id, agent_session_id)
		WHERE id = @id AND org_id = @org_id`

	ct, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":               threadID,
		"org_id":           orgID,
		"current_turn":     turn,
		"agent_session_id": emptyStringNil(agentSessionID),
	})
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("thread not found")
	}
	return nil
}

func emptyStringNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// UpdateResult persists agent results on a thread.
//
// COALESCE on diff preserves the prior thread diff when the current turn did
// not produce one — same rationale as the session-level update; see
// session_store.go updateResultRow for full details.
func (s *SessionThreadStore) UpdateResult(ctx context.Context, orgID, threadID uuid.UUID, status models.ThreadStatus, result *models.SessionResult) error {
	query := `
		UPDATE session_threads
		SET status = @status,
		    completed_at = CASE
		        WHEN @status IN ('completed', 'failed', 'cancelled')
		            THEN now()
		        ELSE completed_at
		    END,
		    confidence_score = @confidence_score,
		    result_summary = @result_summary,
		    diff = COALESCE(@diff, diff),
		    failure_explanation = @failure_explanation,
		    failure_category = @failure_category
		WHERE id = @id AND org_id = @org_id`

	var failureExplanation *string
	if result.Error != nil {
		failureExplanation = result.Error
	}

	ct, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":                  threadID,
		"org_id":              orgID,
		"status":              status,
		"confidence_score":    result.ConfidenceScore,
		"result_summary":      result.ResultSummary,
		"diff":                result.Diff,
		"failure_explanation": failureExplanation,
		"failure_category":    result.FailureCategory,
	})
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("thread not found")
	}
	return nil
}

// FailRunningBySession fails every currently running thread for a terminalized
// session. It is used by reaper paths that fail the parent session outside the
// normal orchestrator/handler completion flow.
func (s *SessionThreadStore) FailRunningBySession(ctx context.Context, orgID, sessionID uuid.UUID, result *models.SessionResult) (int64, error) {
	var failureExplanation *string
	var failureCategory *string
	if result != nil {
		failureExplanation = result.Error
		failureCategory = result.FailureCategory
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE session_threads
		SET status = 'failed',
		    completed_at = now(),
		    failure_explanation = @failure_explanation,
		    failure_category = @failure_category
		WHERE org_id = @org_id AND session_id = @session_id AND status = 'running'`, pgx.NamedArgs{
		"org_id":              orgID,
		"session_id":          sessionID,
		"failure_explanation": failureExplanation,
		"failure_category":    failureCategory,
	})
	if err != nil {
		return 0, fmt.Errorf("fail running session threads: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ListStuckRunningThreads returns threads stuck in status='running' whose
// started_at is older than the given cutoff. The reaper uses this to fail
// rows the orchestrator/handler couldn't reset themselves — typically when
// a continue_session job dead-letters during a rolling deploy and the
// thread reset writes can't reach the DB before the worker process exits.
//
// Mirrors ListStaleRunningSessions in shape and intent: cross-org scan,
// LIMIT 100 to bound a single tick, started_at-based cutoff.
//
// lint:allow-no-orgid reason="cross-org reaper scan for stuck running threads"
func (s *SessionThreadStore) ListStuckRunningThreads(ctx context.Context, startedBefore time.Time) ([]models.SessionThread, error) {
	query := `
		SELECT ` + sessionThreadSelectColumns + `
		FROM session_threads
		WHERE status = 'running'
		  AND started_at IS NOT NULL
		  AND started_at < @started_before
		ORDER BY started_at ASC
		LIMIT 100`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"started_before": startedBefore,
	})
	if err != nil {
		return nil, fmt.Errorf("query stuck running threads: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionThread])
}

// ClaimIdle atomically transitions an idle thread to running. Used when a user
// sends a follow-up message to a specific thread.
func (s *SessionThreadStore) ClaimIdle(ctx context.Context, orgID, threadID uuid.UUID) (models.SessionThread, error) {
	query := `
		UPDATE session_threads
		SET status = 'running'
		WHERE id = @id AND org_id = @org_id AND status = 'idle'
		RETURNING ` + sessionThreadSelectColumns

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     threadID,
		"org_id": orgID,
	})
	if err != nil {
		return models.SessionThread{}, fmt.Errorf("claim idle thread: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionThread])
}

// ErrThreadRunningLimitReached signals that the requested thread is otherwise
// claimable (idle, or in a resumable status), but the session has already
// reached the maximum allowed number of concurrent running siblings.
var ErrThreadRunningLimitReached = fmt.Errorf("session running thread limit reached")

// claimMode names the two ways a thread row can transition into 'running': a
// fresh claim from idle (the start of a new turn) versus a resume from a
// terminal/paused status (continuing after the user sent a follow-up). The
// SET clauses differ structurally between the two — the typed enum keeps the
// branching compiler-checked and ensures the SQL fragment that lands in the
// query never originates from caller-controlled input.
type claimMode int

const (
	claimModeIdle claimMode = iota
	claimModeResume
)

// ClaimIdleForSession atomically transitions an idle thread to running while
// enforcing the session-wide running-thread cap. See claimForSession for the
// shared CTE/cap mechanics.
//
// Returns ErrThreadRunningLimitReached when the target thread is idle but
// adding it would exceed maxRunning. Callers should distinguish this from
// "thread not idle" to render the right composer affordance: in the former
// case, queueing a pending message is appropriate; in the latter, the user is
// trying to send to a tab whose own turn is still in flight.
func (s *SessionThreadStore) ClaimIdleForSession(ctx context.Context, orgID, sessionID, threadID uuid.UUID, maxRunning int) (models.SessionThread, error) {
	return s.claimForSession(ctx, claimForSessionArgs{
		orgID:             orgID,
		sessionID:         sessionID,
		threadID:          threadID,
		maxRunning:        maxRunning,
		mode:              claimModeIdle,
		claimableStatuses: []string{string(models.ThreadStatusIdle)},
	})
}

// ClaimForResumeInSession atomically transitions a thread in a resumable
// terminal/paused status (completed, failed, cancelled, awaiting_input) back
// to running so a follow-up message can continue it. Mirrors
// SessionStore.ClaimForResume at the thread layer; the resumable-status set
// lives in models.ResumableThreadStatuses.
//
// Honors the same session-wide running-thread cap as ClaimIdleForSession and
// surfaces ErrThreadRunningLimitReached the same way, so callers can keep one
// "limit hit, queue instead" branch regardless of which claim path failed.
//
// Resets started_at for the new turn so runtime watchdogs and the stuck-thread
// reaper measure the resumed execution instead of the original thread age.
// completed_at is cleared so the row reflects the new in-flight turn rather
// than the previous terminal state. failure_explanation and failure_category
// are intentionally preserved as audit history of the prior run; mirrors
// SessionStore.ClaimForResume, which also leaves session-level failure fields
// untouched on resume.
func (s *SessionThreadStore) ClaimForResumeInSession(ctx context.Context, orgID, sessionID, threadID uuid.UUID, maxRunning int) (models.SessionThread, error) {
	return s.claimForSession(ctx, claimForSessionArgs{
		orgID:             orgID,
		sessionID:         sessionID,
		threadID:          threadID,
		maxRunning:        maxRunning,
		mode:              claimModeResume,
		claimableStatuses: threadStatusStrings(models.ResumableThreadStatuses),
	})
}

// claimForSessionArgs bundles inputs for claimForSession so each call site
// stays readable and additions don't ripple across positional argument lists.
type claimForSessionArgs struct {
	orgID, sessionID, threadID uuid.UUID
	maxRunning                 int
	// mode selects the per-claim SET clause; see claimModeSetClause.
	mode claimMode
	// claimableStatuses is the set of current thread statuses from which the
	// claim is allowed to fire. ClaimIdleForSession passes ['idle']; the
	// resume path passes models.ResumableThreadStatuses.
	claimableStatuses []string
}

// claimModeSetClause returns the mode-specific SQL fragment that goes into
// the UPDATE SET clause of claimForSession's query. Concentrating the mapping
// here keeps the query template free of branch-on-mode logic and makes it
// trivially evident to a reader (and to a static analyzer) that no
// caller-controlled string ever lands in the SQL.
func claimModeSetClause(mode claimMode) string {
	switch mode {
	case claimModeIdle:
		// Fresh claim of an idle row — record when this turn started so
		// time-on-task surfaces have a precise begin stamp.
		return "started_at = now(),"
	case claimModeResume:
		// Resume of a terminal/paused row — refresh started_at for this turn
		// and clear stale completed_at from the previous turn.
		return "started_at = now(),\n\t\t    completed_at = NULL,"
	default:
		// Unreachable in normal code paths; panic is preferable to silently
		// emitting an UPDATE that does the wrong thing.
		panic(fmt.Sprintf("claimForSession: unknown claimMode %d", mode))
	}
}

// claimForSession is the shared core of ClaimIdleForSession and
// ClaimForResumeInSession. The CTE locks every thread row in the session
// before evaluating sibling state so concurrent sends to different tabs
// serialize on the database row locks. Allows up to maxRunning sibling tabs
// (including this one) to be active at the same time; callers should pass
// models.MaxRunningThreadsPerSession.
//
// Cap-math note: running_count excludes the target via id <> @id. A target
// that is itself in an active status (awaiting_input is the only such case
// in the resumable set) therefore does not count toward siblings — which is
// correct, because resuming such a target does not increase the session's
// total active occupancy. The cap is enforced strictly against siblings.
func (s *SessionThreadStore) claimForSession(ctx context.Context, args claimForSessionArgs) (models.SessionThread, error) {
	maxRunning := args.maxRunning
	if maxRunning <= 0 {
		maxRunning = models.MaxRunningThreadsPerSession
	}
	query := `
		WITH locked_threads AS (
			SELECT id, status
			FROM session_threads
			WHERE org_id = @org_id AND session_id = @session_id AND archived_at IS NULL
			FOR UPDATE
		), target_claimable AS (
			SELECT 1
			FROM locked_threads
			WHERE id = @id AND status = ANY(@claimable_statuses)
		), running_count AS (
			SELECT count(*) AS n
			FROM locked_threads
			WHERE id <> @id
			  AND status IN ('pending', 'running', 'awaiting_input')
		), eligible AS (
			-- target is claimable AND adding it (siblings + 1) stays at-or-under
			-- max_running, i.e. siblings_active < max_running.
			SELECT 1
			FROM target_claimable, running_count
			WHERE running_count.n < @max_running
		)
		UPDATE session_threads
		SET status = 'running',
		    ` + claimModeSetClause(args.mode) + `
		    last_activity_at = now(),
		    cancel_requested_at = NULL
		WHERE id = @id
		  AND org_id = @org_id
		  AND session_id = @session_id
		  AND archived_at IS NULL
		  AND EXISTS (SELECT 1 FROM eligible)
		RETURNING ` + sessionThreadSelectColumns

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":                 args.threadID,
		"org_id":             args.orgID,
		"session_id":         args.sessionID,
		"max_running":        maxRunning,
		"claimable_statuses": args.claimableStatuses,
	})
	if err != nil {
		return models.SessionThread{}, fmt.Errorf("claim session thread: %w", err)
	}
	thread, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionThread])
	if err != nil {
		// pgx.ErrNoRows means the WHERE-with-EXISTS predicate did not match.
		// Best-effort distinguish "limit reached" from "status not claimable"
		// by re-inspecting state without holding the FOR UPDATE lock. A
		// sibling can transition between the failed claim and this lookup; on
		// that race we surface the original ErrNoRows and the caller maps it
		// to "thread not claimable in this status". Acceptable trade-off: the
		// worst case is the user sees the busy-tab affordance instead of the
		// queued-message one for one cycle, then retries.
		if errors.Is(err, pgx.ErrNoRows) {
			limited, lookupErr := s.isAtRunningLimit(ctx, args.orgID, args.sessionID, args.threadID, maxRunning, args.claimableStatuses)
			if lookupErr == nil && limited {
				return models.SessionThread{}, ErrThreadRunningLimitReached
			}
		}
		return models.SessionThread{}, err
	}
	return thread, nil
}

// isAtRunningLimit returns true when the target thread is in a claimable
// status (so otherwise eligible) but the session already has maxRunning
// sibling threads active. Used by claimForSession to differentiate
// "limit reached" from "status changed under us" without holding the
// FOR UPDATE lock.
//
// Uses COALESCE on the target subquery so a deleted-out-from-under-us row
// scans as the empty string instead of erroring on NULL → string. In that
// case we report "not limited" — the caller will fall through to its
// generic ErrNoRows path which the service maps to ErrThreadNotFound.
func (s *SessionThreadStore) isAtRunningLimit(ctx context.Context, orgID, sessionID, threadID uuid.UUID, maxRunning int, claimableStatuses []string) (bool, error) {
	var targetStatus string
	var siblingActive int
	err := s.db.QueryRow(ctx, `
		SELECT
		    COALESCE((SELECT status FROM session_threads WHERE id = @id AND org_id = @org_id AND archived_at IS NULL), '') AS target_status,
		    (SELECT count(*) FROM session_threads
		        WHERE org_id = @org_id AND session_id = @session_id AND id <> @id
		          AND archived_at IS NULL
		          AND status IN ('pending', 'running', 'awaiting_input')) AS sibling_active
	`, pgx.NamedArgs{"id": threadID, "org_id": orgID, "session_id": sessionID}).Scan(&targetStatus, &siblingActive)
	if err != nil {
		return false, err
	}
	if siblingActive < maxRunning {
		return false, nil
	}
	for _, claimable := range claimableStatuses {
		if targetStatus == claimable {
			return true, nil
		}
	}
	return false, nil
}

// CountActive returns the number of threads in pending/running/awaiting_input
// state for a session. Used by the thread service for admission checks and by
// the API to expose running-tab counts to the UI.
func (s *SessionThreadStore) CountActive(ctx context.Context, orgID, sessionID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `
		SELECT count(*)
		FROM session_threads
		WHERE org_id = @org_id AND session_id = @session_id
		  AND archived_at IS NULL
		  AND status IN ('pending', 'running', 'awaiting_input')
	`, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).Scan(&count)
	return count, err
}

// SetBaseSnapshot stamps the thread-start checkpoint key onto the thread row.
// Idempotent: only sets when base_snapshot_key is currently NULL so a
// re-running first turn (e.g. retry after SIGINT) does not overwrite the
// original recovery point. A revert later in the thread's lifetime should
// restore *that* point, not whatever snapshot the most recent retry produced.
func (s *SessionThreadStore) SetBaseSnapshot(ctx context.Context, orgID, threadID uuid.UUID, key string) error {
	if key == "" {
		return nil
	}
	_, err := s.db.Exec(ctx,
		`UPDATE session_threads
		 SET base_snapshot_key = @key
		 WHERE id = @id AND org_id = @org_id AND base_snapshot_key IS NULL`,
		pgx.NamedArgs{"id": threadID, "org_id": orgID, "key": key},
	)
	return err
}

// AddCost increments the thread's cost meter. cost_cents accumulates over the
// thread's lifetime so the UI can surface a per-tab cost without a separate
// query against usage events.
func (s *SessionThreadStore) AddCost(ctx context.Context, orgID, threadID uuid.UUID, cents float64) error {
	if cents <= 0 {
		return nil
	}
	_, err := s.db.Exec(ctx,
		`UPDATE session_threads SET cost_cents = cost_cents + @cents WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": threadID, "org_id": orgID, "cents": cents},
	)
	return err
}

// IncrementPendingMessages is called when a user queues a message on a tab
// that is currently busy with another turn. The composer reads this counter
// to render a "queued (N)" affordance.
func (s *SessionThreadStore) IncrementPendingMessages(ctx context.Context, orgID, threadID uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`UPDATE session_threads SET pending_message_count = pending_message_count + 1 WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": threadID, "org_id": orgID},
	)
	return err
}

// ClearPendingMessages resets the queued-message counter when a turn drains
// its pending queue. Stored value is bounded below at 0 so a missed increment
// does not produce a negative count.
func (s *SessionThreadStore) ClearPendingMessages(ctx context.Context, orgID, threadID uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`UPDATE session_threads SET pending_message_count = 0 WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": threadID, "org_id": orgID},
	)
	return err
}

// MarkCancelRequested stamps the thread with a cancel timestamp so the
// orchestrator and UI can distinguish "user pressed Cancel on this tab" from
// a session-wide cancel. Idempotent: re-cancel does not move the timestamp.
func (s *SessionThreadStore) MarkCancelRequested(ctx context.Context, orgID, threadID uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`UPDATE session_threads
		 SET cancel_requested_at = COALESCE(cancel_requested_at, now())
		 WHERE id = @id AND org_id = @org_id AND status IN ('pending', 'running', 'awaiting_input')`,
		pgx.NamedArgs{"id": threadID, "org_id": orgID},
	)
	return err
}

// UpdateTurnComplete sets the thread to idle and persists turn metadata.
// COALESCE on diff: see UpdateResult above.
func (s *SessionThreadStore) UpdateTurnComplete(ctx context.Context, orgID, threadID uuid.UUID, turn int, result *models.SessionResult, agentSessionID string) error {
	query := `
		UPDATE session_threads
		SET status = 'idle', current_turn = @current_turn, last_activity_at = now(),
		    agent_session_id = @agent_session_id,
		    confidence_score = @confidence_score,
		    result_summary = @result_summary,
		    diff = COALESCE(@diff, diff)
		WHERE id = @id AND org_id = @org_id`

	ct, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":               threadID,
		"org_id":           orgID,
		"current_turn":     turn,
		"agent_session_id": agentSessionID,
		"confidence_score": result.ConfidenceScore,
		"result_summary":   result.ResultSummary,
		"diff":             result.Diff,
	})
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("thread not found")
	}
	return nil
}
