package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type SessionReviewLoopStore struct {
	db DBTX
}

type StrandedPublicationReviewLoop struct {
	LoopID   uuid.UUID `db:"loop_id"`
	ThreadID uuid.UUID `db:"thread_id"`
}

func (s *SessionReviewLoopStore) GetPrimaryChangesetID(ctx context.Context, orgID, sessionID uuid.UUID) (uuid.UUID, error) {
	var changesetID uuid.UUID
	err := s.db.QueryRow(ctx, `SELECT id FROM session_changesets
		WHERE org_id = @org_id AND session_id = @session_id AND is_primary`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID,
	}).Scan(&changesetID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("get primary changeset for review loop: %w", err)
	}
	return changesetID, nil
}

// ListStrandedPublicationLoops returns review loops whose agent thread has
// been inactive past the recovery cutoff and has no live continuation job. The
// linked publication predicate prevents manual/automation loops and detached
// historical rows from being restarted by publication reconciliation.
func (s *SessionReviewLoopStore) ListStrandedPublicationLoops(
	ctx context.Context,
	orgID uuid.UUID,
	inactiveBefore time.Time,
	limit int,
) ([]StrandedPublicationReviewLoop, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `
		SELECT loop.id AS loop_id, loop.thread_id
		FROM session_review_loops AS loop
		JOIN session_threads AS thread
		  ON thread.org_id = loop.org_id AND thread.id = loop.thread_id
		JOIN session_publications AS publication
		  ON publication.org_id = loop.org_id AND publication.review_loop_id = loop.id
		WHERE loop.org_id = @org_id
		  AND loop.source = 'publication' AND loop.status = 'running'
		  AND publication.state = 'review_pending'
		  AND publication.review_gate_state = 'pending'
		  AND thread.status IN ('idle', 'completed', 'failed', 'cancelled')
		  AND COALESCE(thread.last_activity_at, loop.started_at) < @inactive_before
		  AND NOT EXISTS (
			SELECT 1
			FROM jobs
			WHERE jobs.org_id = loop.org_id
			  AND jobs.job_type = 'continue_session'
			  AND jobs.status IN ('pending', 'running')
			  AND jobs.payload->>'thread_id' = loop.thread_id::text
		  )
		ORDER BY COALESCE(thread.last_activity_at, loop.started_at), loop.id
		LIMIT @limit`, pgx.NamedArgs{
		"org_id": orgID, "inactive_before": inactiveBefore, "limit": limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list stranded publication review loops: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[StrandedPublicationReviewLoop])
}

// RestartStrandedPublicationLoop atomically retires one stranded loop, clears
// its stale evidence link, and requeues the original open_pr request. The new
// worker attempt checkpoints the current workspace and starts a fresh review,
// preserving any fixes the prior review wrote before it became stranded.
func (s *SessionReviewLoopStore) RestartStrandedPublicationLoop(
	ctx context.Context,
	orgID, loopID uuid.UUID,
	inactiveBefore time.Time,
	summary string,
) (bool, error) {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return false, fmt.Errorf("restart stranded publication review requires transaction support")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin stranded publication review restart: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var sessionID uuid.UUID
	err = tx.QueryRow(ctx, `
		UPDATE session_review_loops AS loop
		SET status = 'failed',
			latest_summary = @summary,
			completed_passes = (
				SELECT count(*)
				FROM session_review_loop_passes
				WHERE org_id = @org_id AND loop_id = @loop_id
				  AND status IN ('clean', 'needs_fix')
			),
			completed_at = now()
		FROM session_threads AS thread
		WHERE loop.org_id = @org_id AND loop.id = @loop_id
		  AND loop.source = 'publication' AND loop.status = 'running'
		  AND thread.org_id = loop.org_id AND thread.id = loop.thread_id
		  AND thread.status IN ('idle', 'completed', 'failed', 'cancelled')
		  AND COALESCE(thread.last_activity_at, loop.started_at) < @inactive_before
		  AND NOT EXISTS (
			SELECT 1
			FROM jobs
			WHERE jobs.org_id = loop.org_id
			  AND jobs.job_type = 'continue_session'
			  AND jobs.status IN ('pending', 'running')
			  AND jobs.payload->>'thread_id' = loop.thread_id::text
		  )
		RETURNING loop.session_id`, pgx.NamedArgs{
		"org_id": orgID, "loop_id": loopID, "inactive_before": inactiveBefore, "summary": summary,
	}).Scan(&sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("retire stranded publication review loop: %w", err)
	}

	var payload json.RawMessage
	var queue models.SessionPublicationJobQueue
	var changesetID uuid.UUID
	err = tx.QueryRow(ctx, `
		UPDATE session_publications
		SET state = 'review_pending', review_gate_state = 'pending',
			review_loop_id = NULL, review_workspace_revision = NULL,
			review_desired_head_sha = NULL, updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id
		  AND review_loop_id = @loop_id
		  AND state = 'review_pending'
		  AND review_gate_state = 'pending'
		RETURNING request_payload, job_queue, changeset_id`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "loop_id": loopID,
	}).Scan(&payload, &queue, &changesetID)
	if err != nil {
		return false, fmt.Errorf("reset stranded publication review intent: %w", err)
	}
	dedupeKey := OpenPRDedupeKey(changesetID)
	if _, err := enqueueOn(ctx, tx, orgID, EnqueueOpts{
		Queue: string(queue), JobType: "open_pr", Payload: payload, Priority: 5, DedupeKey: &dedupeKey,
	}); err != nil {
		return false, fmt.Errorf("requeue stranded publication review: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit stranded publication review restart: %w", err)
	}
	return true, nil
}

func NewSessionReviewLoopStore(db DBTX) *SessionReviewLoopStore {
	return &SessionReviewLoopStore{db: db}
}

const reviewLoopSelectColumns = `id, org_id, session_id, automation_run_id, thread_id,
	status, source, changeset_id, workspace_revision, desired_head_sha,
	agent_type, max_passes, fix_mode, completed_passes, review_required,
	bypassed_by_user_id, bypass_reason, loop_start_checkpoint_key, latest_checkpoint_key,
	latest_summary, started_by_user_id, started_at, completed_at`

// #nosec G101 -- these are database column names; *_message_id fields are not credentials.
const reviewLoopPassSelectColumns = `id, org_id, loop_id, session_id, pass_index,
	review_message_id, decision_message_id, fix_message_id, status, agent_decision,
	review_output, fix_summary, review_started_at, review_completed_at,
	fix_started_at, fix_completed_at, summary`

func (s *SessionReviewLoopStore) CreateLoop(ctx context.Context, loop *models.SessionReviewLoop) error {
	return createLoopOn(ctx, s.db, loop)
}

func (s *SessionReviewLoopStore) CreateLoopWithInitialPass(ctx context.Context, loop *models.SessionReviewLoop, pass *models.SessionReviewLoopPass) error {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return fmt.Errorf("create review loop with initial pass requires transaction support")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin review loop start tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := createLoopOn(ctx, tx, loop); err != nil {
		return err
	}
	pass.LoopID = loop.ID
	if err := createPassOn(ctx, tx, pass); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit review loop start tx: %w", err)
	}
	return nil
}

func createLoopOn(ctx context.Context, q DBTX, loop *models.SessionReviewLoop) error {
	if err := loop.Status.Validate(); err != nil {
		return err
	}
	if err := loop.Source.Validate(); err != nil {
		return err
	}
	if err := loop.AgentType.Validate(); err != nil {
		return err
	}
	if loop.FixMode == "" {
		loop.FixMode = models.ReviewLoopFixModeMinimal
	}
	if err := loop.FixMode.Validate(); err != nil {
		return err
	}
	query := `
		INSERT INTO session_review_loops (
			org_id, session_id, automation_run_id, thread_id, status, source,
			changeset_id, workspace_revision, desired_head_sha, agent_type,
			max_passes, fix_mode, completed_passes, review_required, bypassed_by_user_id, bypass_reason,
			loop_start_checkpoint_key, latest_checkpoint_key, latest_summary, started_by_user_id
		) VALUES (
			@org_id, @session_id, @automation_run_id, @thread_id, @status, @source,
			@changeset_id, @workspace_revision, @desired_head_sha, @agent_type,
			@max_passes, @fix_mode, @completed_passes, @review_required, @bypassed_by_user_id, @bypass_reason,
			@loop_start_checkpoint_key, @latest_checkpoint_key, @latest_summary, @started_by_user_id
		)
		RETURNING id, started_at`
	err := q.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":                    loop.OrgID,
		"session_id":                loop.SessionID,
		"automation_run_id":         loop.AutomationRunID,
		"thread_id":                 loop.ThreadID,
		"status":                    loop.Status,
		"source":                    loop.Source,
		"changeset_id":              loop.ChangesetID,
		"workspace_revision":        loop.WorkspaceRevision,
		"desired_head_sha":          loop.DesiredHeadSHA,
		"agent_type":                loop.AgentType,
		"max_passes":                loop.MaxPasses,
		"fix_mode":                  loop.FixMode,
		"completed_passes":          loop.CompletedPasses,
		"review_required":           loop.ReviewRequired,
		"bypassed_by_user_id":       loop.BypassedByUserID,
		"bypass_reason":             loop.BypassReason,
		"loop_start_checkpoint_key": loop.LoopStartCheckpointKey,
		"latest_checkpoint_key":     loop.LatestCheckpointKey,
		"latest_summary":            loop.LatestSummary,
		"started_by_user_id":        loop.StartedByUserID,
	}).Scan(&loop.ID, &loop.StartedAt)
	if err != nil {
		return fmt.Errorf("create review loop: %w", err)
	}
	return nil
}

func (s *SessionReviewLoopStore) GetLoopByID(ctx context.Context, orgID, loopID uuid.UUID) (models.SessionReviewLoop, error) {
	query := `SELECT ` + reviewLoopSelectColumns + ` FROM session_review_loops WHERE id = @id AND org_id = @org_id`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"id": loopID, "org_id": orgID})
	if err != nil {
		return models.SessionReviewLoop{}, fmt.Errorf("query review loop: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionReviewLoop])
}

func (s *SessionReviewLoopStore) ListLoopsBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionReviewLoop, error) {
	query := `
		SELECT ` + reviewLoopSelectColumns + `
		FROM session_review_loops
		WHERE org_id = @org_id AND session_id = @session_id
		ORDER BY started_at DESC, id DESC`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID})
	if err != nil {
		return nil, fmt.Errorf("query review loops: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionReviewLoop])
}

func (s *SessionReviewLoopStore) GetRunningLoopBySession(ctx context.Context, orgID, sessionID uuid.UUID) (models.SessionReviewLoop, error) {
	query := `
		SELECT ` + reviewLoopSelectColumns + `
		FROM session_review_loops
		WHERE org_id = @org_id AND session_id = @session_id AND status = 'running'
		ORDER BY started_at DESC, id DESC
		LIMIT 1`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID})
	if err != nil {
		return models.SessionReviewLoop{}, fmt.Errorf("query running review loop by session: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionReviewLoop])
}

func (s *SessionReviewLoopStore) GetRunningLoopByThread(ctx context.Context, orgID, threadID uuid.UUID) (models.SessionReviewLoop, error) {
	query := `
		SELECT ` + reviewLoopSelectColumns + `
		FROM session_review_loops
		WHERE org_id = @org_id AND thread_id = @thread_id AND status = 'running'
		ORDER BY started_at DESC, id DESC
		LIMIT 1`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "thread_id": threadID})
	if err != nil {
		return models.SessionReviewLoop{}, fmt.Errorf("query running review loop: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionReviewLoop])
}

func (s *SessionReviewLoopStore) GetLatestLoopByAutomationRun(ctx context.Context, orgID, automationRunID uuid.UUID) (models.SessionReviewLoop, error) {
	query := `
		SELECT ` + reviewLoopSelectColumns + `
		FROM session_review_loops
		WHERE org_id = @org_id AND automation_run_id = @automation_run_id
		ORDER BY started_at DESC, id DESC
		LIMIT 1`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "automation_run_id": automationRunID})
	if err != nil {
		return models.SessionReviewLoop{}, fmt.Errorf("query automation review loop: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionReviewLoop])
}

func (s *SessionReviewLoopStore) GetFreshCleanPublicationLoop(
	ctx context.Context,
	orgID, sessionID, changesetID uuid.UUID,
	workspaceRevision int64,
	desiredHeadSHA string,
) (models.SessionReviewLoop, error) {
	query := `SELECT ` + reviewLoopSelectColumns + `
		FROM session_review_loops
		WHERE org_id = @org_id AND session_id = @session_id
		  AND changeset_id = @changeset_id
		  AND workspace_revision = @workspace_revision
		  AND desired_head_sha = @desired_head_sha
		  AND source = 'publication' AND status = 'clean'
		ORDER BY completed_at DESC, id DESC
		LIMIT 1`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
		"workspace_revision": workspaceRevision, "desired_head_sha": desiredHeadSHA,
	})
	if err != nil {
		return models.SessionReviewLoop{}, fmt.Errorf("query fresh clean publication review: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionReviewLoop])
}

func (s *SessionReviewLoopStore) RefreshPublicationEvidence(ctx context.Context, orgID, loopID uuid.UUID, workspaceRevision int64, desiredHeadSHA string) error {
	if desiredHeadSHA == "" {
		return fmt.Errorf("publication review desired head SHA is required")
	}
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return fmt.Errorf("refresh publication review evidence requires transaction support")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin publication evidence refresh: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := tx.Exec(ctx, `UPDATE session_review_loops
		SET workspace_revision = @workspace_revision, desired_head_sha = @desired_head_sha
		WHERE org_id = @org_id AND id = @loop_id AND source = 'publication' AND status = 'running'`, pgx.NamedArgs{
		"org_id": orgID, "loop_id": loopID, "workspace_revision": workspaceRevision,
		"desired_head_sha": desiredHeadSHA,
	})
	if err != nil {
		return fmt.Errorf("refresh review loop publication evidence: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	result, err = tx.Exec(ctx, `UPDATE session_publications
		SET review_workspace_revision = @workspace_revision,
			review_desired_head_sha = @desired_head_sha,
			desired_head_sha = @desired_head_sha,
			updated_at = now()
		WHERE org_id = @org_id AND review_loop_id = @loop_id
		  AND state NOT IN ('completed', 'completed_noop', 'terminal_failed')`, pgx.NamedArgs{
		"org_id": orgID, "loop_id": loopID, "workspace_revision": workspaceRevision,
		"desired_head_sha": desiredHeadSHA,
	})
	if err != nil {
		return fmt.Errorf("refresh publication linked evidence: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit publication evidence refresh: %w", err)
	}
	return nil
}

func (s *SessionReviewLoopStore) CreatePass(ctx context.Context, pass *models.SessionReviewLoopPass) error {
	return createPassOn(ctx, s.db, pass)
}

func createPassOn(ctx context.Context, q DBTX, pass *models.SessionReviewLoopPass) error {
	if err := pass.Status.Validate(); err != nil {
		return err
	}
	query := `
		INSERT INTO session_review_loop_passes (
			org_id, loop_id, session_id, pass_index, review_message_id, decision_message_id,
			fix_message_id, status, agent_decision, review_output, fix_summary, summary,
			review_started_at, review_completed_at, fix_started_at, fix_completed_at
		) VALUES (
			@org_id, @loop_id, @session_id, @pass_index, @review_message_id, @decision_message_id,
			@fix_message_id, @status, @agent_decision, @review_output, @fix_summary, @summary,
			COALESCE(@review_started_at, now()), @review_completed_at, @fix_started_at, @fix_completed_at
		)
		RETURNING id, review_started_at`
	var reviewStartedAt time.Time
	err := q.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":              pass.OrgID,
		"loop_id":             pass.LoopID,
		"session_id":          pass.SessionID,
		"pass_index":          pass.PassIndex,
		"review_message_id":   pass.ReviewMessageID,
		"decision_message_id": pass.DecisionMessageID,
		"fix_message_id":      pass.FixMessageID,
		"status":              pass.Status,
		"agent_decision":      pass.AgentDecision,
		"review_output":       pass.ReviewOutput,
		"fix_summary":         pass.FixSummary,
		"summary":             pass.Summary,
		"review_started_at":   pass.ReviewStartedAt,
		"review_completed_at": pass.ReviewCompletedAt,
		"fix_started_at":      pass.FixStartedAt,
		"fix_completed_at":    pass.FixCompletedAt,
	}).Scan(&pass.ID, &reviewStartedAt)
	if err != nil {
		return fmt.Errorf("create review loop pass: %w", err)
	}
	pass.ReviewStartedAt = &reviewStartedAt
	return nil
}

func (s *SessionReviewLoopStore) GetLatestPass(ctx context.Context, orgID, loopID uuid.UUID) (models.SessionReviewLoopPass, error) {
	query := `
		SELECT ` + reviewLoopPassSelectColumns + `
		FROM session_review_loop_passes
		WHERE org_id = @org_id AND loop_id = @loop_id
		ORDER BY pass_index DESC
		LIMIT 1`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "loop_id": loopID})
	if err != nil {
		return models.SessionReviewLoopPass{}, fmt.Errorf("query review loop pass: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionReviewLoopPass])
}

func (s *SessionReviewLoopStore) ListPassesByLoop(ctx context.Context, orgID, loopID uuid.UUID) ([]models.SessionReviewLoopPass, error) {
	query := `
		SELECT ` + reviewLoopPassSelectColumns + `
		FROM session_review_loop_passes
		WHERE org_id = @org_id AND loop_id = @loop_id
		ORDER BY pass_index ASC`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "loop_id": loopID})
	if err != nil {
		return nil, fmt.Errorf("query review loop passes: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionReviewLoopPass])
}

func (s *SessionReviewLoopStore) SetPassReviewMessage(ctx context.Context, orgID, passID uuid.UUID, messageID int64) error {
	return s.execPassUpdate(ctx, `
		UPDATE session_review_loop_passes
		SET review_message_id = @message_id
		WHERE id = @id AND org_id = @org_id`, orgID, passID, pgx.NamedArgs{"message_id": messageID})
}

func (s *SessionReviewLoopStore) MarkPassDeciding(ctx context.Context, orgID, passID uuid.UUID, reviewOutput string, decisionMessageID int64) error {
	return s.execPassUpdate(ctx, `
		UPDATE session_review_loop_passes
		SET status = 'deciding',
		    review_output = @review_output,
		    decision_message_id = @decision_message_id,
		    review_completed_at = now(),
		    summary = @review_output
		WHERE id = @id AND org_id = @org_id`, orgID, passID, pgx.NamedArgs{
		"review_output":       reviewOutput,
		"decision_message_id": decisionMessageID,
	})
}

func (s *SessionReviewLoopStore) MarkPassFixing(ctx context.Context, orgID, passID uuid.UUID, decision models.ReviewLoopDecision, fixMessageID int64) error {
	if err := decision.Validate(); err != nil {
		return err
	}
	return s.execPassUpdate(ctx, `
		UPDATE session_review_loop_passes
		SET status = 'fixing',
		    agent_decision = @agent_decision,
		    fix_message_id = @fix_message_id,
		    fix_started_at = now()
		WHERE id = @id AND org_id = @org_id`, orgID, passID, pgx.NamedArgs{
		"agent_decision": decision,
		"fix_message_id": fixMessageID,
	})
}

func (s *SessionReviewLoopStore) MarkPassClean(ctx context.Context, orgID, loopID, passID uuid.UUID, decision models.ReviewLoopDecision, summary string) error {
	if err := decision.Validate(); err != nil {
		return err
	}
	return s.withTerminalTransition(ctx, func(tx pgx.Tx) error {
		if err := execPassUpdateOn(ctx, tx, `
			UPDATE session_review_loop_passes
			SET status = 'clean', agent_decision = @agent_decision,
			    review_output = COALESCE(review_output, @summary),
			    review_completed_at = COALESCE(review_completed_at, now()), summary = @summary
			WHERE id = @id AND org_id = @org_id`, orgID, passID, pgx.NamedArgs{
			"agent_decision": decision, "summary": summary,
		}); err != nil {
			return err
		}
		return markLoopTerminalOn(ctx, tx, orgID, loopID, models.ReviewLoopStatusClean, summary)
	})
}

func (s *SessionReviewLoopStore) MarkPassCleanAndEnqueueOpenPR(ctx context.Context, orgID, loopID, passID uuid.UUID, decision models.ReviewLoopDecision, summary string, payload map[string]any, dedupeKey string) error {
	if err := decision.Validate(); err != nil {
		return err
	}
	return s.withTerminalOpenPRJob(ctx, orgID, payload, dedupeKey, func(tx pgx.Tx) error {
		if err := execPassUpdateOn(ctx, tx, `
			UPDATE session_review_loop_passes
			SET status = 'clean',
			    agent_decision = @agent_decision,
			    review_output = COALESCE(review_output, @summary),
			    review_completed_at = COALESCE(review_completed_at, now()),
			    summary = @summary
			WHERE id = @id AND org_id = @org_id`, orgID, passID, pgx.NamedArgs{
			"agent_decision": decision,
			"summary":        summary,
		}); err != nil {
			return err
		}
		return markLoopTerminalOn(ctx, tx, orgID, loopID, models.ReviewLoopStatusClean, summary)
	})
}

func (s *SessionReviewLoopStore) MarkPassFixComplete(ctx context.Context, orgID, passID uuid.UUID, fixSummary string) error {
	return s.execPassUpdate(ctx, `
		UPDATE session_review_loop_passes
		SET status = 'needs_fix',
		    fix_summary = @fix_summary,
		    fix_completed_at = now(),
		    summary = @fix_summary
		WHERE id = @id AND org_id = @org_id`, orgID, passID, pgx.NamedArgs{"fix_summary": fixSummary})
}

func (s *SessionReviewLoopStore) MarkLoopNeedsHumanDecision(ctx context.Context, orgID, loopID uuid.UUID, summary string) error {
	return s.withTerminalTransition(ctx, func(tx pgx.Tx) error {
		return markLoopTerminalOn(ctx, tx, orgID, loopID, models.ReviewLoopStatusNeedsHumanDecision, summary)
	})
}

func (s *SessionReviewLoopStore) MarkPassNeedsHumanDecision(ctx context.Context, orgID, loopID, passID uuid.UUID, decision models.ReviewLoopDecision, summary string) error {
	if err := decision.Validate(); err != nil {
		return err
	}
	return s.withTerminalTransition(ctx, func(tx pgx.Tx) error {
		if err := markPassNeedsHumanDecisionOn(ctx, tx, orgID, passID, decision, summary); err != nil {
			return err
		}
		return markLoopTerminalOn(ctx, tx, orgID, loopID, models.ReviewLoopStatusNeedsHumanDecision, summary)
	})
}

func (s *SessionReviewLoopStore) MarkPassNeedsHumanDecisionAndEnqueueOpenPR(ctx context.Context, orgID, loopID, passID uuid.UUID, decision models.ReviewLoopDecision, summary string, payload map[string]any, dedupeKey string) error {
	if err := decision.Validate(); err != nil {
		return err
	}
	return s.withTerminalOpenPRJob(ctx, orgID, payload, dedupeKey, func(tx pgx.Tx) error {
		if err := markPassNeedsHumanDecisionOn(ctx, tx, orgID, passID, decision, summary); err != nil {
			return err
		}
		return markLoopTerminalOn(ctx, tx, orgID, loopID, models.ReviewLoopStatusNeedsHumanDecision, summary)
	})
}

func (s *SessionReviewLoopStore) MarkLoopFailed(ctx context.Context, orgID, loopID uuid.UUID, summary string) error {
	return s.withTerminalTransition(ctx, func(tx pgx.Tx) error {
		return markLoopTerminalOn(ctx, tx, orgID, loopID, models.ReviewLoopStatusFailed, summary)
	})
}

func (s *SessionReviewLoopStore) MarkLoopFailedAndEnqueueOpenPR(ctx context.Context, orgID, loopID uuid.UUID, summary string, payload map[string]any, dedupeKey string) error {
	return s.withTerminalOpenPRJob(ctx, orgID, payload, dedupeKey, func(tx pgx.Tx) error {
		return markLoopTerminalOn(ctx, tx, orgID, loopID, models.ReviewLoopStatusFailed, summary)
	})
}

func markPassNeedsHumanDecisionOn(ctx context.Context, q DBTX, orgID, passID uuid.UUID, decision models.ReviewLoopDecision, summary string) error {
	return execPassUpdateOn(ctx, q, `
		UPDATE session_review_loop_passes
		SET status = 'needs_fix',
		    agent_decision = @agent_decision,
		    summary = @summary
		WHERE id = @id AND org_id = @org_id`, orgID, passID, pgx.NamedArgs{
		"agent_decision": decision,
		"summary":        summary,
	})
}

func (s *SessionReviewLoopStore) CancelLoop(ctx context.Context, orgID, loopID uuid.UUID) error {
	return s.withTerminalTransition(ctx, func(tx pgx.Tx) error {
		return markLoopTerminalOn(ctx, tx, orgID, loopID, models.ReviewLoopStatusCancelled, "Review loop cancelled.")
	})
}

func (s *SessionReviewLoopStore) execPassUpdate(ctx context.Context, query string, orgID, passID uuid.UUID, extra pgx.NamedArgs) error {
	return execPassUpdateOn(ctx, s.db, query, orgID, passID, extra)
}

func execPassUpdateOn(ctx context.Context, q DBTX, query string, orgID, passID uuid.UUID, extra pgx.NamedArgs) error {
	args := pgx.NamedArgs{"id": passID, "org_id": orgID}
	for k, v := range extra {
		args[k] = v
	}
	ct, err := q.Exec(ctx, query, args)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func markLoopTerminalOn(ctx context.Context, q DBTX, orgID, loopID uuid.UUID, status models.ReviewLoopStatus, summary string) error {
	if err := status.Validate(); err != nil {
		return err
	}
	var sessionID uuid.UUID
	var source models.ReviewLoopSource
	err := q.QueryRow(ctx, `
		UPDATE session_review_loops
		SET status = @status,
		    latest_summary = @summary,
		    completed_passes = (
		        SELECT count(*)
		        FROM session_review_loop_passes
		        WHERE org_id = @org_id AND loop_id = @id AND status IN ('clean', 'needs_fix')
		    ),
		    completed_at = now()
		WHERE id = @id AND org_id = @org_id AND status = 'running'
		RETURNING session_id, source`,
		pgx.NamedArgs{"id": loopID, "org_id": orgID, "status": status, "summary": summary}).Scan(&sessionID, &source)
	if err != nil {
		return err
	}
	if source == models.ReviewLoopSourcePublication {
		if err := finishPublicationReviewOn(ctx, q, orgID, loopID, status); err != nil {
			return err
		}
	}
	// Any terminal loop frees the session-wide review slot, so a publication
	// parked behind it can start its own review — regardless of which kind of
	// loop just ended, or how it ended.
	return resumeParkedPublicationOn(ctx, q, orgID, sessionID)
}

func finishPublicationReviewOn(ctx context.Context, q DBTX, orgID, loopID uuid.UUID, status models.ReviewLoopStatus) error {
	// The gate value alone must determine whether the publication is terminal,
	// because that is all a reader has: SetReviewGate makes 'failed' terminal,
	// so this writer must too, or the same gate would mean "retryable" from one
	// path and "settled" from the other. A cancelled loop is not a failure —
	// somebody stopped it, so it needs a human decision and stays live.
	gate, state := models.SessionPublicationReviewGateFailed, models.SessionPublicationStateTerminalFailed
	switch status {
	case models.ReviewLoopStatusClean:
		gate, state = models.SessionPublicationReviewGatePassed, models.SessionPublicationStateReadyToPublish
	case models.ReviewLoopStatusNeedsHumanDecision, models.ReviewLoopStatusCancelled:
		gate, state = models.SessionPublicationReviewGateNeedsHuman, models.SessionPublicationStateReviewPending
	}
	var payload json.RawMessage
	var queue models.SessionPublicationJobQueue
	var changesetID uuid.UUID
	err := q.QueryRow(ctx, `UPDATE session_publications AS publication
		SET review_gate_state = @gate, state = @state,
			completed_at = CASE
				WHEN @state = 'terminal_failed' THEN COALESCE(publication.completed_at, now())
				ELSE publication.completed_at
			END,
			updated_at = now()
		-- session and changeset are joined for the clean-evidence comparison
		-- below. They are keyed to the publication here so the join stays
		-- bounded on every status, including the ones that read nothing from
		-- them; do not drop these predicates when simplifying the OR.
		FROM session_review_loops AS loop, sessions AS session, session_changesets AS changeset
		WHERE publication.org_id = @org_id AND publication.review_loop_id = @loop_id
		  AND loop.org_id = publication.org_id AND loop.id = publication.review_loop_id
		  AND session.org_id = publication.org_id AND session.id = publication.session_id
		  AND changeset.org_id = publication.org_id AND changeset.session_id = publication.session_id
		  AND changeset.id = publication.changeset_id
		  AND (
			-- Only a clean result must prove its evidence is still current. A
			-- non-clean result blocks its linked publication unconditionally:
			-- declining to block on drifted evidence would leave the intent
			-- pending against a loop that has already finished, which no
			-- recovery path can see.
			@status <> 'clean'
			OR (
				publication.review_workspace_revision = loop.workspace_revision
				AND publication.review_desired_head_sha = loop.desired_head_sha
				AND session.workspace_revision = loop.workspace_revision
				AND changeset.head_sha = loop.desired_head_sha
			)
		  )
		  AND publication.state NOT IN ('completed', 'completed_noop', 'terminal_failed')
		RETURNING publication.request_payload, publication.job_queue, publication.changeset_id`, pgx.NamedArgs{
		"org_id": orgID, "loop_id": loopID, "gate": gate, "state": state, "status": status,
	}).Scan(&payload, &queue, &changesetID)
	if errors.Is(err, pgx.ErrNoRows) {
		if status != models.ReviewLoopStatusClean {
			return nil
		}
		// Clean evidence that no longer matches the current session revision
		// and pushed changeset head is stale. Clear the link and atomically
		// resume the original request so a new publication review is started.
		err = q.QueryRow(ctx, `UPDATE session_publications
			SET review_gate_state = 'pending', state = 'review_pending',
				review_loop_id = NULL, review_workspace_revision = NULL,
				review_desired_head_sha = NULL, updated_at = now()
			WHERE org_id = @org_id AND review_loop_id = @loop_id
			  AND state NOT IN ('completed', 'completed_noop', 'terminal_failed')
			RETURNING request_payload, job_queue, changeset_id`, pgx.NamedArgs{
			"org_id": orgID, "loop_id": loopID,
		}).Scan(&payload, &queue, &changesetID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("invalidate stale publication review: %w", err)
		}
		dedupeKey := OpenPRDedupeKey(changesetID)
		if _, err := enqueueOn(ctx, q, orgID, EnqueueOpts{
			Queue: string(queue), JobType: "open_pr", Payload: payload, Priority: 5, DedupeKey: &dedupeKey,
		}); err != nil {
			return fmt.Errorf("enqueue publication after stale review: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("advance publication review gate: %w", err)
	}
	if status != models.ReviewLoopStatusClean {
		return nil
	}
	dedupeKey := OpenPRDedupeKey(changesetID)
	if _, err := enqueueOn(ctx, q, orgID, EnqueueOpts{
		Queue: string(queue), JobType: "open_pr", Payload: payload, Priority: 5, DedupeKey: &dedupeKey,
	}); err != nil {
		return fmt.Errorf("enqueue publication after clean review: %w", err)
	}
	return nil
}

func resumeParkedPublicationOn(ctx context.Context, q DBTX, orgID, sessionID uuid.UUID) error {
	var payload json.RawMessage
	var queue models.SessionPublicationJobQueue
	var changesetID uuid.UUID
	err := q.QueryRow(ctx, `SELECT request_payload, job_queue, changeset_id
		FROM session_publications
		WHERE org_id = @org_id AND session_id = @session_id
		  AND state = 'review_pending' AND review_gate_state = 'pending' AND review_loop_id IS NULL
		ORDER BY requested_at, id
		LIMIT 1`, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).Scan(&payload, &queue, &changesetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load parked publication review: %w", err)
	}
	dedupeKey := OpenPRDedupeKey(changesetID)
	if _, err := enqueueOn(ctx, q, orgID, EnqueueOpts{
		Queue: string(queue), JobType: "open_pr", Payload: payload, Priority: 5, DedupeKey: &dedupeKey,
	}); err != nil {
		return fmt.Errorf("resume parked publication review: %w", err)
	}
	return nil
}

func (s *SessionReviewLoopStore) withTerminalOpenPRJob(ctx context.Context, orgID uuid.UUID, payload map[string]any, dedupeKey string, transition func(pgx.Tx) error) error {
	return s.withTerminalTransition(ctx, func(tx pgx.Tx) error {
		if err := transition(tx); err != nil {
			return err
		}
		jobDedupeKey := dedupeKey
		if _, err := enqueueOn(ctx, tx, orgID, EnqueueOpts{
			Queue:     "default",
			JobType:   "open_pr",
			Payload:   payload,
			Priority:  5,
			DedupeKey: &jobDedupeKey,
		}); err != nil {
			return fmt.Errorf("enqueue open_pr after review loop: %w", err)
		}
		return nil
	})
}

func (s *SessionReviewLoopStore) withTerminalTransition(ctx context.Context, transition func(pgx.Tx) error) error {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return fmt.Errorf("review loop terminal transition requires transaction support")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin review loop terminal tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := transition(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit review loop terminal tx: %w", err)
	}
	return nil
}
