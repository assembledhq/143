package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type SessionReviewLoopStore struct {
	db DBTX
}

func NewSessionReviewLoopStore(db DBTX) *SessionReviewLoopStore {
	return &SessionReviewLoopStore{db: db}
}

const reviewLoopSelectColumns = `id, org_id, session_id, automation_run_id, thread_id,
	status, source, agent_type, max_passes, completed_passes, review_required,
	bypassed_by_user_id, bypass_reason, loop_start_checkpoint_key, latest_checkpoint_key,
	latest_summary, started_by_user_id, started_at, completed_at`

const reviewLoopPassSelectColumns = `id, org_id, loop_id, session_id, pass_index,
	review_message_id, decision_message_id, fix_message_id, status, agent_decision,
	review_output, fix_summary, review_started_at, review_completed_at,
	fix_started_at, fix_completed_at, summary`

func (s *SessionReviewLoopStore) CreateLoop(ctx context.Context, loop *models.SessionReviewLoop) error {
	if err := loop.Status.Validate(); err != nil {
		return err
	}
	if err := loop.Source.Validate(); err != nil {
		return err
	}
	if err := loop.AgentType.Validate(); err != nil {
		return err
	}
	query := `
		INSERT INTO session_review_loops (
			org_id, session_id, automation_run_id, thread_id, status, source, agent_type,
			max_passes, completed_passes, review_required, bypassed_by_user_id, bypass_reason,
			loop_start_checkpoint_key, latest_checkpoint_key, latest_summary, started_by_user_id
		) VALUES (
			@org_id, @session_id, @automation_run_id, @thread_id, @status, @source, @agent_type,
			@max_passes, @completed_passes, @review_required, @bypassed_by_user_id, @bypass_reason,
			@loop_start_checkpoint_key, @latest_checkpoint_key, @latest_summary, @started_by_user_id
		)
		RETURNING id, started_at`
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":                    loop.OrgID,
		"session_id":                loop.SessionID,
		"automation_run_id":         loop.AutomationRunID,
		"thread_id":                 loop.ThreadID,
		"status":                    loop.Status,
		"source":                    loop.Source,
		"agent_type":                loop.AgentType,
		"max_passes":                loop.MaxPasses,
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

func (s *SessionReviewLoopStore) CreatePass(ctx context.Context, pass *models.SessionReviewLoopPass) error {
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
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
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
	if err := execPassUpdateOn(ctx, s.db, `
		UPDATE session_review_loop_passes
		SET status = 'clean',
		    agent_decision = @agent_decision,
		    summary = @summary
		WHERE id = @id AND org_id = @org_id`, orgID, passID, pgx.NamedArgs{
		"agent_decision": decision,
		"summary":        summary,
	}); err != nil {
		return err
	}
	return s.markLoopTerminal(ctx, orgID, loopID, models.ReviewLoopStatusClean, summary)
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
	return s.markLoopTerminal(ctx, orgID, loopID, models.ReviewLoopStatusNeedsHumanDecision, summary)
}

func (s *SessionReviewLoopStore) MarkLoopNeedsHumanDecisionAndEnqueueOpenPR(ctx context.Context, orgID, loopID uuid.UUID, summary string, payload map[string]any, dedupeKey string) error {
	return s.withTerminalOpenPRJob(ctx, orgID, payload, dedupeKey, func(tx pgx.Tx) error {
		return markLoopTerminalOn(ctx, tx, orgID, loopID, models.ReviewLoopStatusNeedsHumanDecision, summary)
	})
}

func (s *SessionReviewLoopStore) MarkLoopFailed(ctx context.Context, orgID, loopID uuid.UUID, summary string) error {
	return s.markLoopTerminal(ctx, orgID, loopID, models.ReviewLoopStatusFailed, summary)
}

func (s *SessionReviewLoopStore) CancelLoop(ctx context.Context, orgID, loopID uuid.UUID) error {
	return s.markLoopTerminal(ctx, orgID, loopID, models.ReviewLoopStatusCancelled, "Review loop cancelled.")
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

func (s *SessionReviewLoopStore) markLoopTerminal(ctx context.Context, orgID, loopID uuid.UUID, status models.ReviewLoopStatus, summary string) error {
	return markLoopTerminalOn(ctx, s.db, orgID, loopID, status, summary)
}

func markLoopTerminalOn(ctx context.Context, q DBTX, orgID, loopID uuid.UUID, status models.ReviewLoopStatus, summary string) error {
	if err := status.Validate(); err != nil {
		return err
	}
	ct, err := q.Exec(ctx, `
		UPDATE session_review_loops
		SET status = @status,
		    latest_summary = @summary,
		    completed_passes = (
		        SELECT count(*)
		        FROM session_review_loop_passes
		        WHERE org_id = @org_id AND loop_id = @id AND status IN ('clean', 'needs_fix')
		    ),
		    completed_at = now()
		WHERE id = @id AND org_id = @org_id AND status = 'running'`,
		pgx.NamedArgs{"id": loopID, "org_id": orgID, "status": status, "summary": summary})
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *SessionReviewLoopStore) withTerminalOpenPRJob(ctx context.Context, orgID uuid.UUID, payload map[string]any, dedupeKey string, transition func(pgx.Tx) error) error {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return fmt.Errorf("review loop terminal open_pr enqueue requires transaction support")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin review loop terminal tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

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
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit review loop terminal tx: %w", err)
	}
	return nil
}
