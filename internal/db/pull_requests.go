package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type PullRequestStore struct {
	db DBTX
}

func NewPullRequestStore(db DBTX) *PullRequestStore {
	return &PullRequestStore{db: db}
}

type PullRequestFilters struct {
	Status models.PullRequestStatus
	Limit  int
	Cursor string
}

type PullRequestGitHubSnapshot struct {
	GitHubPRURL string
	Title       string
	Body        *string
	HeadSHA     *string
	HeadRef     *string
	BaseSHA     *string
}

func (s *PullRequestStore) Create(ctx context.Context, pr *models.PullRequest) error {
	if pr.ChangesetID == nil {
		query := `
			INSERT INTO pull_requests (session_id, changeset_id, org_id, github_pr_number, github_pr_url, github_repo, title, body, status, review_status, authored_by, head_sha, head_ref, base_sha)
			VALUES (
				@session_id,
				CASE WHEN @session_id::uuid IS NULL THEN NULL ELSE (
					SELECT id FROM session_changesets
					WHERE org_id = @org_id AND session_id = @session_id AND is_primary
				) END,
				@org_id, @github_pr_number, @github_pr_url, @github_repo, @title, @body,
				@status, @review_status, @authored_by, @head_sha, @head_ref, @base_sha
			)
			RETURNING id, created_at, updated_at`
		authoredBy := pr.AuthoredBy
		if authoredBy == "" {
			authoredBy = models.GitIdentitySourceApp
		}
		return s.db.QueryRow(ctx, query, pgx.NamedArgs{
			"session_id": pr.SessionID, "org_id": pr.OrgID, "github_pr_number": pr.GitHubPRNumber,
			"github_pr_url": pr.GitHubPRURL, "github_repo": pr.GitHubRepo, "title": pr.Title,
			"body": pr.Body, "status": pr.Status, "review_status": pr.ReviewStatus, "authored_by": authoredBy,
			"head_sha": pr.HeadSHA, "head_ref": pr.HeadRef, "base_sha": pr.BaseSHA,
		}).Scan(&pr.ID, &pr.CreatedAt, &pr.UpdatedAt)
	}
	query := `
		INSERT INTO pull_requests (session_id, changeset_id, org_id, github_pr_number, github_pr_url, github_repo, title, body, status, review_status, authored_by, head_sha, head_ref, base_sha)
		VALUES (@session_id, @changeset_id, @org_id, @github_pr_number, @github_pr_url, @github_repo, @title, @body, @status, @review_status, @authored_by, @head_sha, @head_ref, @base_sha)
		RETURNING id, created_at, updated_at`

	authoredBy := pr.AuthoredBy
	if authoredBy == "" {
		authoredBy = models.GitIdentitySourceApp
	}
	args := pgx.NamedArgs{
		"session_id":       pr.SessionID,
		"changeset_id":     pr.ChangesetID,
		"org_id":           pr.OrgID,
		"github_pr_number": pr.GitHubPRNumber,
		"github_pr_url":    pr.GitHubPRURL,
		"github_repo":      pr.GitHubRepo,
		"title":            pr.Title,
		"body":             pr.Body,
		"status":           pr.Status,
		"review_status":    pr.ReviewStatus,
		"authored_by":      authoredBy,
		"head_sha":         pr.HeadSHA,
		"head_ref":         pr.HeadRef,
		"base_sha":         pr.BaseSHA,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&pr.ID, &pr.CreatedAt, &pr.UpdatedAt)
}

const prSelectColumns = `id, session_id, changeset_id, org_id, github_pr_number, github_pr_url, github_repo,
		       title, body, status, review_status, authored_by, ci_status, head_sha, head_ref, base_sha,
		       merge_state, has_conflicts, failing_test_count, needs_agent_action, github_state_synced_at,
		       health_version, merge_when_ready_state, merge_when_ready_requested_by, merge_when_ready_requested_at,
		       merge_when_ready_head_sha, merge_when_ready_health_version, merge_when_ready_error,
		       merge_when_ready_updated_at, feedback_monitoring, feedback_bot_epoch,
		       feedback_bot_cycles_in_epoch, merged_at, created_at, updated_at`

const prMergeWhenReadyStatusColumns = `merge_when_ready_state, merge_when_ready_requested_by,
	merge_when_ready_requested_at, merge_when_ready_head_sha, merge_when_ready_health_version,
	merge_when_ready_error`

func (s *PullRequestStore) GetByID(ctx context.Context, orgID, id uuid.UUID) (models.PullRequest, error) {
	query := `
		SELECT ` + prSelectColumns + `
		FROM pull_requests
		WHERE id = @id AND org_id = @org_id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
	})
	if err != nil {
		return models.PullRequest{}, fmt.Errorf("query pull request: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.PullRequest])
}

func (s *PullRequestStore) GetBySessionID(ctx context.Context, orgID, sessionID uuid.UUID) (models.PullRequest, error) {
	return s.GetPrimaryBySessionID(ctx, orgID, sessionID)
}

// GetPrimaryBySessionID returns the PR attached to the session's primary
// changeset. Use changeset-scoped lookups for multi-PR behavior; this method is
// the explicit compatibility path for legacy one-PR session surfaces.
func (s *PullRequestStore) GetPrimaryBySessionID(ctx context.Context, orgID, sessionID uuid.UUID) (models.PullRequest, error) {
	query := `
		SELECT ` + prSelectColumns + `
		FROM pull_requests
		WHERE session_id = @session_id AND org_id = @org_id
		  AND (
			changeset_id IS NULL
			OR changeset_id = (
				SELECT id FROM session_changesets
				WHERE org_id = @org_id AND session_id = @session_id AND is_primary
			)
		  )
		ORDER BY
			(changeset_id = (
				SELECT id FROM session_changesets
				WHERE org_id = @org_id AND session_id = @session_id AND is_primary
			)) DESC NULLS LAST,
			created_at DESC
		LIMIT 1`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"session_id": sessionID,
		"org_id":     orgID,
	})
	if err != nil {
		return models.PullRequest{}, fmt.Errorf("query pull request by session: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.PullRequest])
}

func (s *PullRequestStore) GetByChangesetID(ctx context.Context, orgID, sessionID, changesetID uuid.UUID) (models.PullRequest, error) {
	rows, err := s.db.Query(ctx, `SELECT `+prSelectColumns+`
		FROM pull_requests
		WHERE org_id = @org_id AND session_id = @session_id AND changeset_id = @changeset_id`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
	})
	if err != nil {
		return models.PullRequest{}, fmt.Errorf("query pull request by changeset: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.PullRequest])
}

func (s *PullRequestStore) UpdateStatus(ctx context.Context, orgID, id uuid.UUID, status models.PullRequestStatus) error {
	query := `UPDATE pull_requests SET status = @status, updated_at = now() WHERE id = @id AND org_id = @org_id`
	if status == models.PullRequestStatusMerged {
		query = `UPDATE pull_requests SET status = @status, merged_at = now(), updated_at = now() WHERE id = @id AND org_id = @org_id`
	}
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
		"status": status,
	})
	return err
}

func (s *PullRequestStore) UpdateTitle(ctx context.Context, orgID, id uuid.UUID, title string) error {
	query := `UPDATE pull_requests SET title = @title, updated_at = now() WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
		"title":  title,
	})
	return err
}

func (s *PullRequestStore) UpdateGitHubSnapshot(ctx context.Context, orgID, id uuid.UUID, snapshot PullRequestGitHubSnapshot) error {
	query := `UPDATE pull_requests
		SET github_pr_url = @github_pr_url,
		    title = @title,
		    body = @body,
		    head_sha = @head_sha,
		    head_ref = @head_ref,
		    base_sha = @base_sha,
		    github_state_synced_at = CASE WHEN head_sha IS DISTINCT FROM @head_sha THEN NULL ELSE github_state_synced_at END,
		    health_version = CASE WHEN head_sha IS DISTINCT FROM @head_sha THEN 0 ELSE health_version END,
		    merge_state = CASE WHEN head_sha IS DISTINCT FROM @head_sha THEN @merge_state ELSE merge_state END,
		    has_conflicts = CASE WHEN head_sha IS DISTINCT FROM @head_sha THEN false ELSE has_conflicts END,
		    failing_test_count = CASE WHEN head_sha IS DISTINCT FROM @head_sha THEN 0 ELSE failing_test_count END,
		    needs_agent_action = CASE WHEN head_sha IS DISTINCT FROM @head_sha THEN false ELSE needs_agent_action END,
		    updated_at = now()
		WHERE id = @id AND org_id = @org_id`
	res, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":            id,
		"org_id":        orgID,
		"github_pr_url": snapshot.GitHubPRURL,
		"title":         snapshot.Title,
		"body":          snapshot.Body,
		"head_sha":      snapshot.HeadSHA,
		"head_ref":      snapshot.HeadRef,
		"base_sha":      snapshot.BaseSHA,
		"merge_state":   models.PullRequestMergeStateUnknown,
	})
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// UpdateHeadSHA persists the SHA of the most recent commit pushed to the PR's
// head branch. Called after a "Push changes" follow-up push lands so the PR
// row reflects the new HEAD without waiting for the GitHub webhook to round-
// trip. Last-write-wins with the webhook is intentional — both writes carry
// the same SHA after a successful push.
//
// Beyond head_sha, this also resets every column whose value was derived from
// the *previous* HEAD: github_state_synced_at, health_version, merge_state,
// has_conflicts, failing_test_count, and needs_agent_action. A fresh push
// invalidates each of them — a previously-conflicted PR may now be clean,
// previously-failing tests must re-run on the new SHA, and any pending
// "agent action" tag (e.g. a fix-tests prompt) was scoped to the prior
// commit. The next sync_pull_request_state job repopulates them from GitHub.
// Stale signals are intentionally cleared rather than preserved so the UI
// does not surface a misleading "needs action" banner against a commit that
// no longer exists at HEAD.
//
// Returns pgx.ErrNoRows if no PR row matched (org_id/id mismatch or PR was
// deleted between push and update). Callers can detect drift instead of
// silently swallowing a no-op write.
func (s *PullRequestStore) UpdateHeadSHA(ctx context.Context, orgID, id uuid.UUID, headSHA string) error {
	query := `UPDATE pull_requests
		SET head_sha = @head_sha,
		    github_state_synced_at = NULL,
		    health_version = 0,
		    merge_state = @merge_state,
		    has_conflicts = false,
		    failing_test_count = 0,
		    needs_agent_action = false,
		    updated_at = now()
		WHERE id = @id AND org_id = @org_id`
	res, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":          id,
		"org_id":      orgID,
		"head_sha":    headSHA,
		"merge_state": models.PullRequestMergeStateUnknown,
	})
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// GetByRepoAndNumber looks up a PR by repo and number without org scoping.
// This is intentionally org-agnostic because it is called from GitHub webhook
// handlers where no org context exists. The returned pr.OrgID is used for
// subsequent org-scoped operations.
// lint:allow-no-orgid reason="GitHub webhook lookup; no org context pre-auth"
func (s *PullRequestStore) GetByRepoAndNumber(ctx context.Context, repo string, number int) (models.PullRequest, error) {
	query := `
		SELECT ` + prSelectColumns + `
		FROM pull_requests
		WHERE github_repo = @github_repo AND github_pr_number = @github_pr_number`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"github_repo":      repo,
		"github_pr_number": number,
	})
	if err != nil {
		return models.PullRequest{}, fmt.Errorf("query pull request by repo and number: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.PullRequest])
}

func (s *PullRequestStore) GetByOrgRepoAndNumber(ctx context.Context, orgID uuid.UUID, repo string, number int) (models.PullRequest, error) {
	query := `
		SELECT ` + prSelectColumns + `
		FROM pull_requests
		WHERE org_id = @org_id AND github_repo = @github_repo AND github_pr_number = @github_pr_number`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":           orgID,
		"github_repo":      repo,
		"github_pr_number": number,
	})
	if err != nil {
		return models.PullRequest{}, fmt.Errorf("query pull request by org, repo, and number: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.PullRequest])
}

func (s *PullRequestStore) ListOpenByOrgRepoAndHeadSHA(ctx context.Context, orgID uuid.UUID, repo, headSHA string) ([]models.PullRequest, error) {
	query := `
		SELECT ` + prSelectColumns + `
		FROM pull_requests
		WHERE org_id = @org_id
		  AND github_repo = @github_repo
		  AND head_sha = @head_sha
		  AND status = 'open'`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":      orgID,
		"github_repo": repo,
		"head_sha":    headSHA,
	})
	if err != nil {
		return nil, fmt.Errorf("query open pull requests by org, repo, and head SHA: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByNameLax[models.PullRequest])
}

func (s *PullRequestStore) UpdateReviewStatus(ctx context.Context, orgID, id uuid.UUID, reviewStatus models.PullRequestReviewStatus) error {
	query := `UPDATE pull_requests SET review_status = @review_status, updated_at = now() WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":            id,
		"org_id":        orgID,
		"review_status": reviewStatus,
	})
	return err
}

func (s *PullRequestStore) ListByOrg(ctx context.Context, orgID uuid.UUID, filters PullRequestFilters) ([]models.PullRequest, error) {
	query := `
		SELECT ` + prSelectColumns + `
		FROM pull_requests
		WHERE org_id = @org_id`

	args := pgx.NamedArgs{"org_id": orgID}

	if filters.Status != "" {
		query += ` AND status = @status`
		args["status"] = filters.Status
	}
	if filters.Cursor != "" {
		cursorID, err := uuid.Parse(filters.Cursor)
		if err == nil {
			query += ` AND id < @cursor_id`
			args["cursor_id"] = cursorID
		}
	}

	query += ` ORDER BY created_at DESC`

	limit := filters.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query += fmt.Sprintf(` LIMIT %d`, limit)

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query pull requests: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByNameLax[models.PullRequest])
}

func (s *PullRequestStore) BatchGetBySessionIDs(ctx context.Context, orgID uuid.UUID, sessionIDs []uuid.UUID) (map[uuid.UUID]models.PullRequest, error) {
	return s.BatchGetPrimaryBySessionIDs(ctx, orgID, sessionIDs)
}

// ListBySessionChangesets returns at most one current PR for each changeset.
// Legacy PRs without a changeset remain represented by the primary changeset
// through the migration backfill, so callers never need a second join path.
func (s *PullRequestStore) ListBySessionChangesets(ctx context.Context, orgID, sessionID uuid.UUID) (map[uuid.UUID]models.PullRequest, error) {
	batched, err := s.BatchListBySessionChangesets(ctx, orgID, []uuid.UUID{sessionID})
	if err != nil {
		return nil, err
	}
	return batched[sessionID], nil
}

// BatchListBySessionChangesets preserves every changeset PR while hydrating
// multiple sessions. The outer key is session ID and the inner key is
// changeset ID; unlike the scalar compatibility helper, no PR is collapsed.
func (s *PullRequestStore) BatchListBySessionChangesets(ctx context.Context, orgID uuid.UUID, sessionIDs []uuid.UUID) (map[uuid.UUID]map[uuid.UUID]models.PullRequest, error) {
	if len(sessionIDs) == 0 {
		return map[uuid.UUID]map[uuid.UUID]models.PullRequest{}, nil
	}
	rows, err := s.db.Query(ctx, `SELECT DISTINCT ON (changeset_id) `+prSelectColumns+`
		FROM pull_requests
		WHERE org_id = @org_id AND session_id = ANY(@session_ids) AND changeset_id IS NOT NULL
		ORDER BY changeset_id, created_at DESC, id DESC`, pgx.NamedArgs{"org_id": orgID, "session_ids": sessionIDs})
	if err != nil {
		return nil, fmt.Errorf("list pull requests by changeset: %w", err)
	}
	prs, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[models.PullRequest])
	if err != nil {
		return nil, fmt.Errorf("collect pull requests by changeset: %w", err)
	}
	result := make(map[uuid.UUID]map[uuid.UUID]models.PullRequest)
	for _, pr := range prs {
		if pr.SessionID != nil && pr.ChangesetID != nil {
			if result[*pr.SessionID] == nil {
				result[*pr.SessionID] = make(map[uuid.UUID]models.PullRequest)
			}
			result[*pr.SessionID][*pr.ChangesetID] = pr
		}
	}
	return result, nil
}

// BatchGetPrimaryBySessionIDs returns only the PR attached to each session's
// primary changeset. It is the scalar compatibility read for session list and
// sidebar surfaces; Phase 2 multi-PR hydration must use a changeset-keyed read.
func (s *PullRequestStore) BatchGetPrimaryBySessionIDs(ctx context.Context, orgID uuid.UUID, sessionIDs []uuid.UUID) (map[uuid.UUID]models.PullRequest, error) {
	if len(sessionIDs) == 0 {
		return nil, nil
	}

	query := `
		SELECT DISTINCT ON (session_id) ` + prSelectColumns + `
		FROM pull_requests
		WHERE org_id = @org_id AND session_id = ANY(@session_ids)
		  AND (
			changeset_id IS NULL
			OR changeset_id = (
				SELECT id FROM session_changesets
				WHERE session_changesets.org_id = pull_requests.org_id
				  AND session_changesets.session_id = pull_requests.session_id
				  AND is_primary
			)
		  )
		ORDER BY session_id,
			(changeset_id IS NOT NULL) DESC,
			created_at DESC,
			id DESC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":      orgID,
		"session_ids": sessionIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("batch query pull requests: %w", err)
	}
	prs, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[models.PullRequest])
	if err != nil {
		return nil, err
	}
	result := make(map[uuid.UUID]models.PullRequest, len(prs))
	for _, pr := range prs {
		if pr.SessionID != nil {
			result[*pr.SessionID] = pr
		}
	}
	return result, nil
}

// UpdateCIStatus updates the CI status of a pull request.
func (s *PullRequestStore) UpdateCIStatus(ctx context.Context, orgID, id uuid.UUID, ciStatus models.PullRequestCIStatus) error {
	query := `UPDATE pull_requests SET ci_status = @ci_status, updated_at = now() WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":        id,
		"org_id":    orgID,
		"ci_status": ciStatus,
	})
	return err
}

func (s *PullRequestStore) QueueMergeWhenReady(ctx context.Context, orgID, id, userID uuid.UUID, headSHA string, healthVersion int64) (models.PullRequestMergeWhenReadyStatus, error) {
	query := `
		UPDATE pull_requests
		SET merge_when_ready_state = @state,
			merge_when_ready_requested_by = @requested_by,
			merge_when_ready_requested_at = now(),
			merge_when_ready_head_sha = @head_sha,
			merge_when_ready_health_version = @health_version,
			merge_when_ready_error = '',
			merge_when_ready_updated_at = now(),
			updated_at = now()
		WHERE id = @id
		  AND org_id = @org_id
		  AND merge_when_ready_state IN ('off', 'queued', 'failed', 'cancelled')
		RETURNING ` + prMergeWhenReadyStatusColumns

	return s.queryMergeWhenReadyStatus(ctx, query, pgx.NamedArgs{
		"id":             id,
		"org_id":         orgID,
		"requested_by":   userID,
		"head_sha":       headSHA,
		"health_version": healthVersion,
		"state":          models.PullRequestMergeWhenReadyStateQueued,
	})
}

func (s *PullRequestStore) CancelMergeWhenReady(ctx context.Context, orgID, id uuid.UUID) (models.PullRequestMergeWhenReadyStatus, error) {
	query := `
		UPDATE pull_requests
		SET merge_when_ready_state = @state,
			merge_when_ready_error = '',
			merge_when_ready_updated_at = now(),
			updated_at = now()
		WHERE id = @id
		  AND org_id = @org_id
		  AND merge_when_ready_state IN ('queued', 'failed', 'cancelled')
		RETURNING ` + prMergeWhenReadyStatusColumns

	return s.queryMergeWhenReadyStatus(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
		"state":  models.PullRequestMergeWhenReadyStateCancelled,
	})
}

func (s *PullRequestStore) ClaimMergeWhenReadyForProcessing(ctx context.Context, orgID, id uuid.UUID, staleBefore time.Time) (bool, error) {
	query := `
		UPDATE pull_requests
		SET merge_when_ready_state = @state,
			merge_when_ready_error = '',
			merge_when_ready_updated_at = now(),
			updated_at = now()
		WHERE id = @id
		  AND org_id = @org_id
		  AND (
			merge_when_ready_state = 'queued'
			OR (
				merge_when_ready_state = 'merging'
				AND merge_when_ready_updated_at < @stale_before
			)
		  )`

	res, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":           id,
		"org_id":       orgID,
		"state":        models.PullRequestMergeWhenReadyStateMerging,
		"stale_before": staleBefore,
	})
	if err != nil {
		return false, err
	}
	return res.RowsAffected() > 0, nil
}

// ReleaseMergeWhenReadyClaim returns a claimed (merging) merge-when-ready
// request to the queued state so it is retried later. Used when a transient
// block — checks still running, mergeability still being computed by GitHub —
// is observed after the claim. It preserves the requesting user, head SHA, and
// health version and clears any prior error. No-op if the row is not currently
// in the merging state (e.g. it was cancelled or superseded meanwhile).
func (s *PullRequestStore) ReleaseMergeWhenReadyClaim(ctx context.Context, orgID, id uuid.UUID) error {
	query := `
		UPDATE pull_requests
		SET merge_when_ready_state = @state,
			merge_when_ready_error = '',
			merge_when_ready_updated_at = now(),
			updated_at = now()
		WHERE id = @id
		  AND org_id = @org_id
		  AND merge_when_ready_state = 'merging'`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
		"state":  models.PullRequestMergeWhenReadyStateQueued,
	})
	return err
}

func (s *PullRequestStore) MarkMergeWhenReadySucceeded(ctx context.Context, orgID, id uuid.UUID) error {
	return s.markMergeWhenReadyTerminal(ctx, orgID, id, models.PullRequestMergeWhenReadyStateSucceeded, "")
}

func (s *PullRequestStore) MarkMergeWhenReadyFailed(ctx context.Context, orgID, id uuid.UUID, reason string) error {
	return s.markMergeWhenReadyTerminal(ctx, orgID, id, models.PullRequestMergeWhenReadyStateFailed, reason)
}

func (s *PullRequestStore) markMergeWhenReadyTerminal(ctx context.Context, orgID, id uuid.UUID, state models.PullRequestMergeWhenReadyState, reason string) error {
	query := `
		UPDATE pull_requests
		SET merge_when_ready_state = @state,
			merge_when_ready_error = @reason,
			merge_when_ready_updated_at = now(),
			updated_at = now()
		WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
		"state":  state,
		"reason": reason,
	})
	return err
}

func (s *PullRequestStore) ListMergeWhenReadyForProcessing(ctx context.Context, orgID uuid.UUID, staleBefore time.Time, limit int) ([]models.PullRequest, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	query := `
		SELECT ` + prSelectColumns + `
		FROM pull_requests
		WHERE org_id = @org_id
		  AND status = 'open'
		  AND (
			merge_when_ready_state = 'queued'
			OR (
				merge_when_ready_state = 'merging'
				AND merge_when_ready_updated_at < @stale_before
			)
		  )
		ORDER BY merge_when_ready_updated_at ASC NULLS FIRST, updated_at ASC
		LIMIT @limit`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":       orgID,
		"stale_before": staleBefore,
		"limit":        limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list merge-when-ready pull requests for processing: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByNameLax[models.PullRequest])
}

func (s *PullRequestStore) queryMergeWhenReadyStatus(ctx context.Context, query string, args pgx.NamedArgs) (models.PullRequestMergeWhenReadyStatus, error) {
	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return models.PullRequestMergeWhenReadyStatus{}, err
	}
	type row struct {
		State                  models.PullRequestMergeWhenReadyState `db:"merge_when_ready_state"`
		RequestedByUserID      *uuid.UUID                            `db:"merge_when_ready_requested_by"`
		RequestedAt            *time.Time                            `db:"merge_when_ready_requested_at"`
		RequestedHeadSHA       string                                `db:"merge_when_ready_head_sha"`
		RequestedHealthVersion *int64                                `db:"merge_when_ready_health_version"`
		LastError              string                                `db:"merge_when_ready_error"`
	}
	statusRow, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[row])
	if err != nil {
		return models.PullRequestMergeWhenReadyStatus{}, err
	}
	return models.PullRequestMergeWhenReadyStatus{
		State:                  statusRow.State,
		RequestedByUserID:      statusRow.RequestedByUserID,
		RequestedAt:            statusRow.RequestedAt,
		RequestedHeadSHA:       statusRow.RequestedHeadSHA,
		RequestedHealthVersion: statusRow.RequestedHealthVersion,
		LastError:              statusRow.LastError,
	}, nil
}
