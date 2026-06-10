package db

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

// EvalBootstrapStore provides CRUD operations for eval_bootstrap_runs.
type EvalBootstrapStore struct {
	db DBTX
}

// NewEvalBootstrapStore returns a new EvalBootstrapStore backed by the given db handle.
func NewEvalBootstrapStore(db DBTX) *EvalBootstrapStore {
	return &EvalBootstrapStore{db: db}
}

// Create inserts a new bootstrap run.
func (s *EvalBootstrapStore) Create(ctx context.Context, run *models.EvalBootstrapRun) error {
	if run.SessionID == nil && run.ThreadID == nil {
		return s.db.QueryRow(ctx,
			`INSERT INTO eval_bootstrap_runs (org_id, repo_id, status, created_by)
			 VALUES (@org_id, @repo_id, @status, @created_by)
			 RETURNING id, created_at`,
			pgx.NamedArgs{
				"org_id":     run.OrgID,
				"repo_id":    run.RepoID,
				"status":     run.Status,
				"created_by": run.CreatedBy,
			},
		).Scan(&run.ID, &run.CreatedAt)
	}
	return s.db.QueryRow(ctx,
		`INSERT INTO eval_bootstrap_runs (org_id, repo_id, status, session_id, thread_id, created_by)
		 VALUES (@org_id, @repo_id, @status, @session_id, @thread_id, @created_by)
		 RETURNING id, created_at`,
		pgx.NamedArgs{
			"org_id":     run.OrgID,
			"repo_id":    run.RepoID,
			"status":     run.Status,
			"session_id": run.SessionID,
			"thread_id":  run.ThreadID,
			"created_by": run.CreatedBy,
		},
	).Scan(&run.ID, &run.CreatedAt)
}

// GetByID retrieves a bootstrap run by ID, scoped to org.
func (s *EvalBootstrapStore) GetByID(ctx context.Context, orgID, id uuid.UUID) (models.EvalBootstrapRun, error) {
	var r models.EvalBootstrapRun
	err := s.db.QueryRow(ctx,
		`SELECT id, org_id, repo_id, status, NULL::jsonb AS candidates, session_id, thread_id, created_by, created_at, completed_at, error_message
		 FROM eval_bootstrap_runs WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": id, "org_id": orgID},
	).Scan(&r.ID, &r.OrgID, &r.RepoID, &r.Status, &r.Candidates, &r.SessionID, &r.ThreadID,
		&r.CreatedBy, &r.CreatedAt, &r.CompletedAt, &r.ErrorMessage)
	if err != nil {
		return r, fmt.Errorf("get eval bootstrap run: %w", err)
	}
	return r, nil
}

// GetLatestByOrg returns the most recent bootstrap run for an org+repo.
func (s *EvalBootstrapStore) GetLatestByOrg(ctx context.Context, orgID, repoID uuid.UUID) (models.EvalBootstrapRun, error) {
	var r models.EvalBootstrapRun
	err := s.db.QueryRow(ctx,
		`SELECT id, org_id, repo_id, status, NULL::jsonb AS candidates, session_id, thread_id, created_by, created_at, completed_at, error_message
		 FROM eval_bootstrap_runs WHERE org_id = @org_id AND repo_id = @repo_id
		 ORDER BY created_at DESC LIMIT 1`,
		pgx.NamedArgs{"org_id": orgID, "repo_id": repoID},
	).Scan(&r.ID, &r.OrgID, &r.RepoID, &r.Status, &r.Candidates, &r.SessionID, &r.ThreadID,
		&r.CreatedBy, &r.CreatedAt, &r.CompletedAt, &r.ErrorMessage)
	if err != nil {
		return r, fmt.Errorf("get latest eval bootstrap run: %w", err)
	}
	return r, nil
}

// GetBySessionThread retrieves the bootstrap run associated with a session-backed bootstrap tool call.
func (s *EvalBootstrapStore) GetBySessionThread(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.EvalBootstrapRun, error) {
	var r models.EvalBootstrapRun
	err := s.db.QueryRow(ctx,
		`SELECT id, org_id, repo_id, status, NULL::jsonb AS candidates, session_id, thread_id, created_by, created_at, completed_at, error_message
		 FROM eval_bootstrap_runs WHERE org_id = @org_id AND session_id = @session_id AND thread_id = @thread_id
		 ORDER BY created_at DESC LIMIT 1`,
		pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "thread_id": threadID},
	).Scan(&r.ID, &r.OrgID, &r.RepoID, &r.Status, &r.Candidates, &r.SessionID, &r.ThreadID,
		&r.CreatedBy, &r.CreatedAt, &r.CompletedAt, &r.ErrorMessage)
	if err != nil {
		return r, fmt.Errorf("get eval bootstrap run by session thread: %w", err)
	}
	return r, nil
}

// UpdateStatus updates the status and optionally sets the session/thread IDs.
func (s *EvalBootstrapStore) UpdateStatus(ctx context.Context, orgID, id uuid.UUID, status models.EvalBootstrapStatus, sessionID *uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE eval_bootstrap_runs SET status = @status, session_id = COALESCE(@session_id, session_id)
		 WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": id, "org_id": orgID, "status": status, "session_id": sessionID},
	)
	if err != nil {
		return fmt.Errorf("update eval bootstrap run status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

const evalBootstrapCandidateColumns = `id, org_id, bootstrap_run_id, session_id, thread_id, repo_id, candidate_index, pr_number, pr_title, base_commit_sha, solution_commit_sha, solution_diff, issue_description, scoring_criteria, complexity, fitness_score, fitness_reasoning, evidence, warnings, payload, status, rejection_reason, created_by_tool, reviewed_by, reviewed_at, accepted_task_id, created_at`

func scanEvalBootstrapCandidate(row pgx.Row) (models.EvalBootstrapCandidateRow, error) {
	var c models.EvalBootstrapCandidateRow
	err := row.Scan(
		&c.ID, &c.OrgID, &c.BootstrapRunID, &c.SessionID, &c.ThreadID, &c.RepoID,
		&c.CandidateIndex, &c.PRNumber, &c.PRTitle, &c.BaseCommitSHA, &c.SolutionCommitSHA,
		&c.SolutionDiff, &c.IssueDescription, &c.ScoringCriteria, &c.Complexity,
		&c.FitnessScore, &c.FitnessReasoning, &c.Evidence, &c.Warnings, &c.Payload,
		&c.Status, &c.RejectionReason, &c.CreatedByTool, &c.ReviewedBy, &c.ReviewedAt,
		&c.AcceptedTaskID, &c.CreatedAt,
	)
	return c, err
}

func scanEvalBootstrapCandidates(rows pgx.Rows) ([]models.EvalBootstrapCandidateRow, error) {
	var candidates []models.EvalBootstrapCandidateRow
	for rows.Next() {
		var c models.EvalBootstrapCandidateRow
		if err := rows.Scan(
			&c.ID, &c.OrgID, &c.BootstrapRunID, &c.SessionID, &c.ThreadID, &c.RepoID,
			&c.CandidateIndex, &c.PRNumber, &c.PRTitle, &c.BaseCommitSHA, &c.SolutionCommitSHA,
			&c.SolutionDiff, &c.IssueDescription, &c.ScoringCriteria, &c.Complexity,
			&c.FitnessScore, &c.FitnessReasoning, &c.Evidence, &c.Warnings, &c.Payload,
			&c.Status, &c.RejectionReason, &c.CreatedByTool, &c.ReviewedBy, &c.ReviewedAt,
			&c.AcceptedTaskID, &c.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan eval bootstrap candidate: %w", err)
		}
		candidates = append(candidates, c)
	}
	return candidates, rows.Err()
}

// CreateCandidate inserts a proposed candidate emitted by a session-backed eval bootstrap agent.
// On concurrent inserts the MAX(candidate_index)+1 computation can collide; up to 3 retries
// are attempted before returning an error.
func (s *EvalBootstrapStore) CreateCandidate(ctx context.Context, candidate *models.EvalBootstrapCandidateRow) error {
	if candidate.Status == "" {
		candidate.Status = models.EvalBootstrapCandidateStatusProposed
	}
	if candidate.CreatedByTool == "" {
		candidate.CreatedByTool = "eval_add"
	}
	if err := hydrateCandidateFields(candidate); err != nil {
		return err
	}
	const maxRetries = 3
	for range maxRetries {
		err := s.tryCreateCandidate(ctx, candidate)
		if err == nil {
			return nil
		}
		if isUniqueViolation(err) {
			continue
		}
		return err
	}
	return fmt.Errorf("create eval bootstrap candidate: too many concurrent retries")
}

func (s *EvalBootstrapStore) tryCreateCandidate(ctx context.Context, candidate *models.EvalBootstrapCandidateRow) error {
	query := `WITH bootstrap_run AS (
		SELECT org_id, repo_id, session_id, thread_id
		FROM eval_bootstrap_runs
		WHERE id = @bootstrap_run_id AND org_id = @org_id AND status = 'running'
	),
	next_candidate AS (
		SELECT COALESCE(MAX(candidate_index) + 1, 0) AS candidate_index
		FROM eval_bootstrap_candidates
		WHERE org_id = @org_id AND bootstrap_run_id = @bootstrap_run_id
	)
	INSERT INTO eval_bootstrap_candidates (
		org_id, bootstrap_run_id, session_id, thread_id, repo_id, candidate_index,
		pr_number, pr_title, base_commit_sha, solution_commit_sha, solution_diff,
		issue_description, scoring_criteria, complexity, fitness_score,
		fitness_reasoning, evidence, warnings, payload, status, created_by_tool
	)
	SELECT
		@org_id, @bootstrap_run_id, bootstrap_run.session_id, bootstrap_run.thread_id,
		bootstrap_run.repo_id, next_candidate.candidate_index,
		@pr_number, @pr_title, @base_commit_sha, @solution_commit_sha, @solution_diff,
		@issue_description, @scoring_criteria, @complexity, @fitness_score,
		@fitness_reasoning, @evidence, @warnings, @payload, @status, @created_by_tool
	FROM bootstrap_run, next_candidate
	RETURNING id, session_id, thread_id, repo_id, candidate_index, status, created_at`
	if err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":              candidate.OrgID,
		"bootstrap_run_id":    candidate.BootstrapRunID,
		"pr_number":           candidate.PRNumber,
		"pr_title":            candidate.PRTitle,
		"base_commit_sha":     candidate.BaseCommitSHA,
		"solution_commit_sha": candidate.SolutionCommitSHA,
		"solution_diff":       candidate.SolutionDiff,
		"issue_description":   candidate.IssueDescription,
		"scoring_criteria":    candidate.ScoringCriteria,
		"complexity":          candidate.Complexity,
		"fitness_score":       candidate.FitnessScore,
		"fitness_reasoning":   candidate.FitnessReasoning,
		"evidence":            candidate.Evidence,
		"warnings":            candidate.Warnings,
		"payload":             candidate.Payload,
		"status":              candidate.Status,
		"created_by_tool":     candidate.CreatedByTool,
	}).Scan(&candidate.ID, &candidate.SessionID, &candidate.ThreadID, &candidate.RepoID,
		&candidate.CandidateIndex, &candidate.Status, &candidate.CreatedAt); err != nil {
		return fmt.Errorf("create eval bootstrap candidate: %w", err)
	}
	return nil
}

func hydrateCandidateFields(candidate *models.EvalBootstrapCandidateRow) error {
	if len(candidate.Evidence) == 0 {
		candidate.Evidence = json.RawMessage(`{}`)
	}
	if candidate.Warnings == nil {
		candidate.Warnings = []string{}
	}
	if candidate.PRNumber == 0 && len(candidate.Payload) > 0 {
		var payload models.EvalBootstrapCandidate
		if err := json.Unmarshal(candidate.Payload, &payload); err != nil {
			return fmt.Errorf("parse eval bootstrap candidate payload: %w", err)
		}
		candidate.PRNumber = payload.PRNumber
		candidate.PRTitle = payload.PRTitle
		candidate.BaseCommitSHA = payload.BaseCommitSHA
		candidate.SolutionCommitSHA = payload.SolutionCommitSHA
		candidate.SolutionDiff = payload.SolutionDiff
		candidate.IssueDescription = payload.IssueDescription
		candidate.Complexity = payload.Complexity
		candidate.FitnessScore = payload.FitnessScore
		candidate.FitnessReasoning = payload.FitnessReasoning
		candidate.Evidence = payload.Evidence
		candidate.Warnings = payload.Warnings
		criteria, err := json.Marshal(payload.ScoringCriteria)
		if err != nil {
			return fmt.Errorf("marshal eval bootstrap candidate criteria: %w", err)
		}
		candidate.ScoringCriteria = criteria
	}
	if len(candidate.Payload) == 0 {
		payload := candidate.Candidate()
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal eval bootstrap candidate payload: %w", err)
		}
		candidate.Payload = raw
	}
	return nil
}

// ListCandidatesByRun lists normalized candidates for a bootstrap run, scoped by org.
func (s *EvalBootstrapStore) ListCandidatesByRun(ctx context.Context, orgID, bootstrapRunID uuid.UUID) ([]models.EvalBootstrapCandidateRow, error) {
	query := fmt.Sprintf(`SELECT %s FROM eval_bootstrap_candidates WHERE org_id = @org_id AND bootstrap_run_id = @bootstrap_run_id ORDER BY created_at ASC`, evalBootstrapCandidateColumns)
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "bootstrap_run_id": bootstrapRunID})
	if err != nil {
		return nil, fmt.Errorf("list eval bootstrap candidates: %w", err)
	}
	defer rows.Close()
	return scanEvalBootstrapCandidates(rows)
}

// GetCandidateByID fetches one normalized candidate by ID, scoped by org.
func (s *EvalBootstrapStore) GetCandidateByID(ctx context.Context, orgID, candidateID uuid.UUID) (models.EvalBootstrapCandidateRow, error) {
	query := fmt.Sprintf(`SELECT %s FROM eval_bootstrap_candidates WHERE id = @id AND org_id = @org_id`, evalBootstrapCandidateColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{"id": candidateID, "org_id": orgID})
	candidate, err := scanEvalBootstrapCandidate(row)
	if err != nil {
		return candidate, fmt.Errorf("get eval bootstrap candidate: %w", err)
	}
	return candidate, nil
}

// MarkCandidateAccepted links a candidate to the eval task created from it.
func (s *EvalBootstrapStore) MarkCandidateAccepted(ctx context.Context, orgID, candidateID, taskID uuid.UUID, reviewedBy *uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE eval_bootstrap_candidates
		 SET status = 'accepted', rejection_reason = NULL, reviewed_by = @reviewed_by, reviewed_at = now(), accepted_task_id = @accepted_task_id
		 WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{"id": candidateID, "org_id": orgID, "accepted_task_id": taskID, "reviewed_by": reviewedBy},
	)
	if err != nil {
		return fmt.Errorf("mark eval bootstrap candidate accepted: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// UpdateCandidateReview stores non-accepting human review state for a candidate.
func (s *EvalBootstrapStore) UpdateCandidateReview(ctx context.Context, orgID, candidateID uuid.UUID, status models.EvalBootstrapCandidateStatus, rejectionReason *string, reviewedBy *uuid.UUID) error {
	if status != models.EvalBootstrapCandidateStatusProposed &&
		status != models.EvalBootstrapCandidateStatusNeedsRevision &&
		status != models.EvalBootstrapCandidateStatusRejected {
		return fmt.Errorf("candidate review status must be proposed, needs_revision, or rejected")
	}
	tag, err := s.db.Exec(ctx,
		`UPDATE eval_bootstrap_candidates
		 SET status = @status, rejection_reason = @rejection_reason, reviewed_by = @reviewed_by, reviewed_at = now()
		 WHERE id = @id AND org_id = @org_id AND status <> 'accepted'`,
		pgx.NamedArgs{
			"id":               candidateID,
			"org_id":           orgID,
			"status":           status,
			"rejection_reason": rejectionReason,
			"reviewed_by":      reviewedBy,
		},
	)
	if err != nil {
		return fmt.Errorf("update eval bootstrap candidate review: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// UpdateResult marks the run as completed or failed. The candidates argument is
// retained for call-site compatibility; normalized candidates are stored in
// eval_bootstrap_candidates.
func (s *EvalBootstrapStore) UpdateResult(ctx context.Context, orgID, id uuid.UUID, status models.EvalBootstrapStatus, candidates []byte, errMsg *string) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE eval_bootstrap_runs
		 SET status = @status, error_message = @error_message, completed_at = now()
		 WHERE id = @id AND org_id = @org_id`,
		pgx.NamedArgs{
			"id":            id,
			"org_id":        orgID,
			"status":        status,
			"error_message": errMsg,
		},
	)
	if err != nil {
		return fmt.Errorf("update eval bootstrap run result: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
