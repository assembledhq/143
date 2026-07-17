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

type SessionPublicationStore struct {
	db DBTX
}

func NewSessionPublicationStore(db DBTX) *SessionPublicationStore {
	return &SessionPublicationStore{db: db}
}

const sessionPublicationSelectColumns = `id, org_id, session_id, changeset_id, repository_id,
	state, source, review_gate_state, job_queue, request_payload, request_generation_at,
	base_branch, head_branch, desired_head_sha,
	published_head_sha, github_pr_number, github_pr_url, attempt_count,
	last_error_code, last_error_message, requested_at, last_attempt_at,
	branch_published_at, pr_resolved_at, completed_at, created_at, updated_at`

func (s *SessionPublicationStore) EnsureRequested(ctx context.Context, orgID uuid.UUID, publication *models.SessionPublication) error {
	if publication == nil {
		return errors.New("session publication is required")
	}
	if publication.OrgID != uuid.Nil && publication.OrgID != orgID {
		return errors.New("session publication org does not match orgID")
	}
	if err := publication.Source.Validate(); err != nil {
		return err
	}
	if err := publication.ReviewGateState.Validate(); err != nil {
		return err
	}
	if publication.JobQueue == "" {
		publication.JobQueue = models.SessionPublicationJobQueueDefault
	}
	if err := publication.JobQueue.Validate(); err != nil {
		return err
	}
	requestPayload := string(publication.RequestPayload)
	if requestPayload == "" {
		requestPayload = "{}"
	}
	if publication.SessionID == uuid.Nil || publication.ChangesetID == uuid.Nil || publication.RepositoryID == uuid.Nil {
		return errors.New("session, changeset, and repository IDs are required")
	}
	if publication.BaseBranch == "" || publication.HeadBranch == "" {
		return errors.New("publication base and head branches are required")
	}
	if publication.RequestGenerationAt.IsZero() {
		publication.RequestGenerationAt = time.Now().UTC()
	}

	rows, err := s.db.Query(ctx, `INSERT INTO session_publications (
		org_id, session_id, changeset_id, repository_id, state, source,
		review_gate_state, job_queue, request_payload, request_generation_at,
		base_branch, head_branch, desired_head_sha
	) VALUES (
		@org_id, @session_id, @changeset_id, @repository_id, 'requested', @source,
		@review_gate_state, @job_queue, @request_payload::jsonb, @request_generation_at,
		@base_branch, @head_branch, @desired_head_sha
	)
	ON CONFLICT (org_id, changeset_id) DO UPDATE SET
		state = CASE
			WHEN session_publications.state IN ('completed_noop', 'terminal_failed')
			 AND EXCLUDED.request_generation_at > session_publications.request_generation_at
			THEN 'requested'
			ELSE session_publications.state
		END,
		repository_id = CASE
			WHEN session_publications.state IN ('completed', 'completed_noop', 'terminal_failed')
			 AND NOT (
				session_publications.state IN ('completed_noop', 'terminal_failed')
				AND EXCLUDED.request_generation_at > session_publications.request_generation_at
			 )
			THEN session_publications.repository_id
			ELSE EXCLUDED.repository_id
		END,
		base_branch = CASE
			WHEN session_publications.state IN ('completed', 'completed_noop', 'terminal_failed')
			 AND NOT (
				session_publications.state IN ('completed_noop', 'terminal_failed')
				AND EXCLUDED.request_generation_at > session_publications.request_generation_at
			 )
			THEN session_publications.base_branch
			ELSE EXCLUDED.base_branch
		END,
		head_branch = CASE
			WHEN session_publications.state IN ('completed', 'completed_noop', 'terminal_failed')
			 AND NOT (
				session_publications.state IN ('completed_noop', 'terminal_failed')
				AND EXCLUDED.request_generation_at > session_publications.request_generation_at
			 )
			THEN session_publications.head_branch
			ELSE EXCLUDED.head_branch
		END,
		desired_head_sha = CASE
			WHEN session_publications.state IN ('completed_noop', 'terminal_failed')
			 AND EXCLUDED.request_generation_at > session_publications.request_generation_at
			THEN EXCLUDED.desired_head_sha
			WHEN session_publications.state IN ('completed', 'completed_noop', 'terminal_failed')
			THEN session_publications.desired_head_sha
			ELSE COALESCE(EXCLUDED.desired_head_sha, session_publications.desired_head_sha)
		END,
		request_generation_at = CASE
			WHEN session_publications.state IN ('completed_noop', 'terminal_failed')
			 AND EXCLUDED.request_generation_at > session_publications.request_generation_at
			THEN EXCLUDED.request_generation_at
			ELSE session_publications.request_generation_at
		END,
		source = CASE
			WHEN session_publications.state IN ('completed_noop', 'terminal_failed')
			 AND EXCLUDED.request_generation_at > session_publications.request_generation_at
			THEN EXCLUDED.source
			WHEN session_publications.state IN ('completed', 'completed_noop', 'terminal_failed')
			THEN session_publications.source
			WHEN session_publications.source IN ('backfill', 'reconciler') THEN EXCLUDED.source
			ELSE session_publications.source
		END,
		review_gate_state = CASE
			WHEN session_publications.state IN ('completed_noop', 'terminal_failed')
			 AND EXCLUDED.request_generation_at > session_publications.request_generation_at
			THEN EXCLUDED.review_gate_state
			WHEN session_publications.state IN ('completed', 'completed_noop', 'terminal_failed')
			THEN session_publications.review_gate_state
			WHEN session_publications.review_gate_state IN ('passed', 'needs_human', 'failed')
			THEN session_publications.review_gate_state
			ELSE EXCLUDED.review_gate_state
		END,
		job_queue = CASE
			WHEN session_publications.state IN ('completed_noop', 'terminal_failed')
			 AND EXCLUDED.request_generation_at > session_publications.request_generation_at
			THEN EXCLUDED.job_queue
			WHEN session_publications.state IN ('completed', 'completed_noop', 'terminal_failed')
			THEN session_publications.job_queue
			WHEN (session_publications.request_payload = '{}'::jsonb
			      OR session_publications.source IN ('backfill', 'reconciler'))
			 AND EXCLUDED.request_payload <> '{}'::jsonb
			THEN EXCLUDED.job_queue
			ELSE session_publications.job_queue
		END,
		request_payload = CASE
			WHEN session_publications.state IN ('completed_noop', 'terminal_failed')
			 AND EXCLUDED.request_generation_at > session_publications.request_generation_at
			THEN EXCLUDED.request_payload
			WHEN session_publications.state IN ('completed', 'completed_noop', 'terminal_failed')
			THEN session_publications.request_payload
			WHEN (session_publications.request_payload = '{}'::jsonb
			      OR session_publications.source IN ('backfill', 'reconciler'))
			 AND EXCLUDED.request_payload <> '{}'::jsonb
			THEN EXCLUDED.request_payload
			ELSE session_publications.request_payload
		END,
		published_head_sha = CASE
			WHEN session_publications.state IN ('completed_noop', 'terminal_failed')
			 AND EXCLUDED.request_generation_at > session_publications.request_generation_at
			THEN NULL ELSE session_publications.published_head_sha
		END,
		github_pr_number = CASE
			WHEN session_publications.state IN ('completed_noop', 'terminal_failed')
			 AND EXCLUDED.request_generation_at > session_publications.request_generation_at
			THEN NULL ELSE session_publications.github_pr_number
		END,
		github_pr_url = CASE
			WHEN session_publications.state IN ('completed_noop', 'terminal_failed')
			 AND EXCLUDED.request_generation_at > session_publications.request_generation_at
			THEN NULL ELSE session_publications.github_pr_url
		END,
		attempt_count = CASE
			WHEN session_publications.state IN ('completed_noop', 'terminal_failed')
			 AND EXCLUDED.request_generation_at > session_publications.request_generation_at
			THEN 0 ELSE session_publications.attempt_count
		END,
		last_error_code = CASE
			WHEN session_publications.state IN ('completed_noop', 'terminal_failed')
			 AND EXCLUDED.request_generation_at > session_publications.request_generation_at
			THEN NULL ELSE session_publications.last_error_code
		END,
		last_error_message = CASE
			WHEN session_publications.state IN ('completed_noop', 'terminal_failed')
			 AND EXCLUDED.request_generation_at > session_publications.request_generation_at
			THEN NULL ELSE session_publications.last_error_message
		END,
		requested_at = CASE
			WHEN session_publications.state IN ('completed_noop', 'terminal_failed')
			 AND EXCLUDED.request_generation_at > session_publications.request_generation_at
			THEN now() ELSE session_publications.requested_at
		END,
		last_attempt_at = CASE
			WHEN session_publications.state IN ('completed_noop', 'terminal_failed')
			 AND EXCLUDED.request_generation_at > session_publications.request_generation_at
			THEN NULL ELSE session_publications.last_attempt_at
		END,
		branch_published_at = CASE
			WHEN session_publications.state IN ('completed_noop', 'terminal_failed')
			 AND EXCLUDED.request_generation_at > session_publications.request_generation_at
			THEN NULL ELSE session_publications.branch_published_at
		END,
		pr_resolved_at = CASE
			WHEN session_publications.state IN ('completed_noop', 'terminal_failed')
			 AND EXCLUDED.request_generation_at > session_publications.request_generation_at
			THEN NULL ELSE session_publications.pr_resolved_at
		END,
		completed_at = CASE
			WHEN session_publications.state IN ('completed_noop', 'terminal_failed')
			 AND EXCLUDED.request_generation_at > session_publications.request_generation_at
			THEN NULL ELSE session_publications.completed_at
		END,
		updated_at = now()
	RETURNING `+sessionPublicationSelectColumns, pgx.NamedArgs{
		"org_id": orgID, "session_id": publication.SessionID, "changeset_id": publication.ChangesetID,
		"repository_id": publication.RepositoryID, "source": publication.Source,
		"review_gate_state": publication.ReviewGateState, "job_queue": publication.JobQueue,
		"request_payload": requestPayload, "request_generation_at": publication.RequestGenerationAt,
		"base_branch": publication.BaseBranch,
		"head_branch": publication.HeadBranch, "desired_head_sha": publication.DesiredHeadSHA,
	})
	if err != nil {
		return fmt.Errorf("ensure session publication: %w", err)
	}
	stored, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionPublication])
	if err != nil {
		return fmt.Errorf("collect session publication: %w", err)
	}
	*publication = stored
	return nil
}

func (s *SessionPublicationStore) GetByChangeset(ctx context.Context, orgID, sessionID, changesetID uuid.UUID) (models.SessionPublication, error) {
	rows, err := s.db.Query(ctx, `SELECT `+sessionPublicationSelectColumns+`
		FROM session_publications
		WHERE org_id = @org_id AND session_id = @session_id AND changeset_id = @changeset_id`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
	})
	if err != nil {
		return models.SessionPublication{}, fmt.Errorf("get session publication: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionPublication])
}

func (s *SessionPublicationStore) ListBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionPublication, error) {
	rows, err := s.db.Query(ctx, `SELECT `+sessionPublicationSelectColumns+`
		FROM session_publications
		WHERE org_id = @org_id AND session_id = @session_id
		ORDER BY requested_at, id`, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID})
	if err != nil {
		return nil, fmt.Errorf("list session publications: %w", err)
	}
	publications, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionPublication])
	if err != nil {
		return nil, fmt.Errorf("collect session publications: %w", err)
	}
	if publications == nil {
		publications = []models.SessionPublication{}
	}
	return publications, nil
}

// StartAttempt atomically records a new publication attempt. It returns false
// when the operation is already terminal so a stale/replayed job can stop
// without re-running any GitHub side effects.
func (s *SessionPublicationStore) StartAttempt(ctx context.Context, orgID, sessionID, changesetID uuid.UUID) (bool, error) {
	result, err := s.db.Exec(ctx, `UPDATE session_publications
		SET attempt_count = attempt_count + 1,
			last_attempt_at = now(),
			last_error_code = NULL,
			last_error_message = NULL,
			state = CASE WHEN state = 'retryable_failed' THEN 'requested' ELSE state END,
			updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND changeset_id = @changeset_id
		  AND state NOT IN ('completed', 'completed_noop', 'terminal_failed')`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
	})
	if err != nil {
		return false, fmt.Errorf("start session publication attempt: %w", err)
	}
	if result.RowsAffected() > 0 {
		return true, nil
	}

	publication, getErr := s.GetByChangeset(ctx, orgID, sessionID, changesetID)
	if getErr != nil {
		return false, getErr
	}
	if publication.State.Terminal() {
		return false, nil
	}
	return false, pgx.ErrNoRows
}

func (s *SessionPublicationStore) SetReviewGate(ctx context.Context, orgID, sessionID, changesetID uuid.UUID, state models.SessionPublicationReviewGateState) error {
	if err := state.Validate(); err != nil {
		return err
	}
	publicationState := models.SessionPublicationStateReviewPending
	if state == models.SessionPublicationReviewGatePassed || state == models.SessionPublicationReviewGateNotRequired {
		publicationState = models.SessionPublicationStateReadyToPublish
	}
	if state == models.SessionPublicationReviewGateFailed {
		publicationState = models.SessionPublicationStateTerminalFailed
	}
	result, err := s.db.Exec(ctx, `UPDATE session_publications
		SET review_gate_state = @review_gate_state,
			state = @state,
			completed_at = CASE WHEN @state = 'terminal_failed' THEN now() ELSE completed_at END,
			updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND changeset_id = @changeset_id
		  AND state IN ('requested', 'review_pending', 'ready_to_publish', 'retryable_failed')`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
		"review_gate_state": state, "state": publicationState,
	})
	if err != nil {
		return fmt.Errorf("update session publication review gate: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *SessionPublicationStore) RecordBranchPublished(ctx context.Context, orgID, sessionID, changesetID uuid.UUID, headSHA string) error {
	result, err := s.db.Exec(ctx, `UPDATE session_publications
		SET state = CASE
				WHEN state IN ('pr_resolved', 'recorded') THEN state
				ELSE 'branch_published'
			END,
			published_head_sha = @head_sha,
			branch_published_at = COALESCE(branch_published_at, now()),
			last_error_code = NULL, last_error_message = NULL, updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND changeset_id = @changeset_id
		  AND state NOT IN ('completed', 'completed_noop', 'terminal_failed')`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID, "head_sha": headSHA,
	})
	if err != nil {
		return fmt.Errorf("record published session branch: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *SessionPublicationStore) RecordPRResolved(ctx context.Context, orgID, sessionID, changesetID uuid.UUID, number int, prURL string) error {
	result, err := s.db.Exec(ctx, `UPDATE session_publications
		SET state = CASE WHEN state = 'recorded' THEN state ELSE 'pr_resolved' END,
			github_pr_number = @number, github_pr_url = @url,
			pr_resolved_at = COALESCE(pr_resolved_at, now()),
			last_error_code = NULL, last_error_message = NULL, updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND changeset_id = @changeset_id
		  AND state NOT IN ('completed', 'completed_noop', 'terminal_failed')`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
		"number": number, "url": prURL,
	})
	if err != nil {
		return fmt.Errorf("record resolved pull request: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *SessionPublicationStore) MarkRecorded(ctx context.Context, orgID, sessionID, changesetID uuid.UUID) error {
	return s.setState(ctx, orgID, sessionID, changesetID, models.SessionPublicationStateRecorded, nil, nil, false)
}

func (s *SessionPublicationStore) MarkCompleted(ctx context.Context, orgID, sessionID, changesetID uuid.UUID) error {
	return s.setState(ctx, orgID, sessionID, changesetID, models.SessionPublicationStateCompleted, nil, nil, true)
}

func (s *SessionPublicationStore) MarkCompletedNoop(ctx context.Context, orgID, sessionID, changesetID uuid.UUID) error {
	return s.setState(ctx, orgID, sessionID, changesetID, models.SessionPublicationStateCompletedNoop, nil, nil, true)
}

func (s *SessionPublicationStore) MarkFailed(ctx context.Context, orgID, sessionID, changesetID uuid.UUID, code, message string, terminal bool) error {
	state := models.SessionPublicationStateRetryableFailed
	if terminal {
		state = models.SessionPublicationStateTerminalFailed
	}
	return s.setState(ctx, orgID, sessionID, changesetID, state, &code, &message, terminal)
}

// RecordRequeued advances a failed publication back into the active state and
// moves it behind the current reconciliation window after its original guarded
// open_pr job has been enqueued again.
func (s *SessionPublicationStore) RecordRequeued(ctx context.Context, orgID, sessionID, changesetID uuid.UUID) error {
	result, err := s.db.Exec(ctx, `UPDATE session_publications
		SET state = CASE WHEN state = 'retryable_failed' THEN 'requested' ELSE state END,
			last_error_code = NULL, last_error_message = NULL, updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND changeset_id = @changeset_id
		  AND state NOT IN ('completed', 'completed_noop', 'terminal_failed')`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
	})
	if err != nil {
		return fmt.Errorf("record requeued session publication: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *SessionPublicationStore) setState(ctx context.Context, orgID, sessionID, changesetID uuid.UUID, state models.SessionPublicationState, errorCode, errorMessage *string, completed bool) error {
	if err := state.Validate(); err != nil {
		return err
	}
	result, err := s.db.Exec(ctx, `UPDATE session_publications
		SET state = @state, last_error_code = @error_code, last_error_message = @error_message,
			completed_at = CASE WHEN @completed THEN COALESCE(completed_at, now()) ELSE completed_at END,
			updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND changeset_id = @changeset_id
		  AND (state NOT IN ('completed', 'completed_noop', 'terminal_failed') OR state = @state)`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
		"state": state, "error_code": errorCode, "error_message": errorMessage, "completed": completed,
	})
	if err != nil {
		return fmt.Errorf("update session publication state: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *SessionPublicationStore) ListReconcileCandidates(ctx context.Context, orgID uuid.UUID, updatedBefore time.Time, limit int) ([]models.SessionPublication, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `SELECT `+sessionPublicationSelectColumns+`
		FROM session_publications
		WHERE org_id = @org_id
		  AND state IN (
			'requested', 'review_pending', 'ready_to_publish',
			'branch_published', 'pr_resolved', 'recorded', 'retryable_failed'
		  )
		  AND (
			review_gate_state IN ('not_required', 'passed')
			OR EXISTS (
				SELECT 1 FROM pull_requests
				WHERE pull_requests.org_id = session_publications.org_id
				  AND pull_requests.session_id = session_publications.session_id
				  AND pull_requests.changeset_id = session_publications.changeset_id
			)
		  )
		  AND updated_at < @updated_before
		ORDER BY updated_at, id
		LIMIT @limit`, pgx.NamedArgs{"org_id": orgID, "updated_before": updatedBefore, "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("list session publication reconciliation candidates: %w", err)
	}
	publications, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionPublication])
	if err != nil {
		return nil, fmt.Errorf("collect session publication reconciliation candidates: %w", err)
	}
	return publications, nil
}
