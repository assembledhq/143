package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const codeReviewDecisionOutcomeColumns = `org_id, session_id, pull_request_id, repository_id,
	policy_id, decision, reason_codes, merged, merged_at, independent_approver_login,
	independent_blocking_review_login, human_review_comment_count, terminal,
	observed_until, provider_reconcile_attempted_at, projection_updated_at, created_at`

type CodeReviewInsightStore struct {
	db TxStarter
}

func NewCodeReviewInsightStore(db TxStarter) *CodeReviewInsightStore {
	return &CodeReviewInsightStore{db: db}
}

// ProjectDecision creates or repairs the denormalized outcome for one completed
// review session. Provider-derived fields survive a replay of this projection.
func (s *CodeReviewInsightStore) ProjectDecision(ctx context.Context, orgID, sessionID uuid.UUID) error {
	return s.projectDecisions(ctx, s.db, orgID, []uuid.UUID{sessionID})
}

func (s *CodeReviewInsightStore) projectDecisions(ctx context.Context, q DBTX, orgID uuid.UUID, sessionIDs []uuid.UUID) error {
	if len(sessionIDs) == 0 {
		return nil
	}
	_, err := q.Exec(ctx, `INSERT INTO code_review_decision_outcomes (
		org_id, session_id, pull_request_id, repository_id, policy_id, decision,
		reason_codes, merged, merged_at, independent_approver_login,
		independent_blocking_review_login, human_review_comment_count, terminal,
		lifecycle_observed_at, observed_until, projection_updated_at, created_at
	)
	SELECT m.org_id, m.session_id, m.pull_request_id, m.repository_id, m.policy_id, m.decision,
		COALESCE(ARRAY(
			SELECT DISTINCT reason->>'code'
			FROM jsonb_array_elements(COALESCE(m.risk_reason_details, '[]'::jsonb)) reason
			WHERE NULLIF(reason->>'code', '') IS NOT NULL
			ORDER BY reason->>'code'
		), '{}'),
		COALESCE(lifecycle.merged, pr.status = 'merged'),
		CASE WHEN lifecycle.pull_request_id IS NULL THEN pr.merged_at ELSE lifecycle.merged_at END,
		(SELECT observation.reviewer_login FROM code_review_human_review_observations observation
		 WHERE observation.org_id = m.org_id AND observation.pull_request_id = m.pull_request_id
		   AND observation.active = true AND observation.independent = true AND observation.state = 'approved'
		 ORDER BY observation.observed_at DESC, observation.github_review_id DESC LIMIT 1),
		(SELECT observation.reviewer_login FROM code_review_human_review_observations observation
		 WHERE observation.org_id = m.org_id AND observation.pull_request_id = m.pull_request_id
		   AND observation.active = true AND observation.independent = true AND observation.state = 'changes_requested'
		 ORDER BY observation.observed_at DESC, observation.github_review_id DESC LIMIT 1),
		(SELECT count(*)::integer FROM review_comments rc
		 WHERE rc.org_id = @org_id AND rc.pull_request_id = m.pull_request_id
		   AND lower(rc.reviewer_type) = 'user'),
		COALESCE(lifecycle.terminal, pr.status IN ('merged', 'closed')), lifecycle.observed_at,
		GREATEST(COALESCE(m.completed_at, m.created_at), COALESCE(lifecycle.observed_at, '-infinity'::timestamptz)),
		now(), COALESCE(m.completed_at, m.created_at)
	FROM code_review_session_metadata m
	JOIN pull_requests pr ON pr.org_id = m.org_id AND pr.id = m.pull_request_id
	LEFT JOIN code_review_pull_request_lifecycle_observations lifecycle
	  ON lifecycle.org_id = m.org_id AND lifecycle.pull_request_id = m.pull_request_id
	WHERE m.org_id = @org_id AND m.session_id = ANY(@session_ids)
	  AND m.status = 'completed' AND m.decision IS NOT NULL
	ON CONFLICT (org_id, session_id) DO UPDATE SET
		decision = EXCLUDED.decision,
		reason_codes = EXCLUDED.reason_codes,
		merged = CASE
			WHEN EXCLUDED.lifecycle_observed_at IS NOT NULL AND
				(code_review_decision_outcomes.lifecycle_observed_at IS NULL OR
				 code_review_decision_outcomes.lifecycle_observed_at <= EXCLUDED.lifecycle_observed_at)
			THEN EXCLUDED.merged ELSE code_review_decision_outcomes.merged END,
		merged_at = CASE
			WHEN EXCLUDED.lifecycle_observed_at IS NOT NULL AND
				(code_review_decision_outcomes.lifecycle_observed_at IS NULL OR
				 code_review_decision_outcomes.lifecycle_observed_at <= EXCLUDED.lifecycle_observed_at)
			THEN EXCLUDED.merged_at ELSE code_review_decision_outcomes.merged_at END,
		terminal = CASE
			WHEN EXCLUDED.lifecycle_observed_at IS NOT NULL AND
				(code_review_decision_outcomes.lifecycle_observed_at IS NULL OR
				 code_review_decision_outcomes.lifecycle_observed_at <= EXCLUDED.lifecycle_observed_at)
			THEN EXCLUDED.terminal ELSE code_review_decision_outcomes.terminal END,
		lifecycle_observed_at = CASE
			WHEN EXCLUDED.lifecycle_observed_at IS NOT NULL AND
				(code_review_decision_outcomes.lifecycle_observed_at IS NULL OR
				 code_review_decision_outcomes.lifecycle_observed_at <= EXCLUDED.lifecycle_observed_at)
			THEN EXCLUDED.lifecycle_observed_at ELSE code_review_decision_outcomes.lifecycle_observed_at END,
		observed_until = GREATEST(code_review_decision_outcomes.observed_until, EXCLUDED.observed_until),
		independent_approver_login = EXCLUDED.independent_approver_login,
		independent_blocking_review_login = EXCLUDED.independent_blocking_review_login,
		human_review_comment_count = EXCLUDED.human_review_comment_count,
		projection_updated_at = now()`, pgx.NamedArgs{"org_id": orgID, "session_ids": sessionIDs})
	if err != nil {
		return fmt.Errorf("project code review decision outcome: %w", err)
	}
	return nil
}

// ProjectRecentDecisions repairs recent completed decisions and all
// non-terminal projections. It is safe to run repeatedly.
func (s *CodeReviewInsightStore) ProjectRecentDecisions(ctx context.Context, orgID uuid.UUID, staleBefore time.Time, limit int) (int64, error) {
	if limit <= 0 || limit > 1000 {
		limit = 250
	}
	rows, err := s.db.Query(ctx, `SELECT m.session_id
		FROM code_review_session_metadata m
		LEFT JOIN code_review_decision_outcomes o
		  ON o.org_id = m.org_id AND o.session_id = m.session_id
		LEFT JOIN code_review_pull_request_lifecycle_observations lifecycle
		  ON lifecycle.org_id = m.org_id AND lifecycle.pull_request_id = m.pull_request_id
		WHERE m.org_id = @org_id AND m.status = 'completed' AND m.decision IS NOT NULL
		  AND (o.session_id IS NULL OR
		       (o.terminal = false AND o.projection_updated_at < @stale_before) OR
		       o.projection_updated_at < m.updated_at OR
		       (lifecycle.observed_at IS NOT NULL AND
		        (o.lifecycle_observed_at IS NULL OR o.lifecycle_observed_at < lifecycle.observed_at)))
		ORDER BY (o.session_id IS NOT NULL), o.projection_updated_at NULLS FIRST,
		         m.completed_at NULLS FIRST, m.session_id
		LIMIT @limit`, pgx.NamedArgs{"org_id": orgID, "stale_before": staleBefore, "limit": limit})
	if err != nil {
		return 0, fmt.Errorf("list code review outcomes requiring projection: %w", err)
	}
	sessionIDs, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, fmt.Errorf("collect code review outcomes requiring projection: %w", err)
	}
	if err := s.projectDecisions(ctx, s.db, orgID, sessionIDs); err != nil {
		return 0, err
	}
	return int64(len(sessionIDs)), nil
}

func (s *CodeReviewInsightStore) projectPullRequestDecisions(ctx context.Context, orgID, pullRequestID uuid.UUID) error {
	rows, err := s.db.Query(ctx, `SELECT session_id FROM code_review_session_metadata
		WHERE org_id = @org_id AND pull_request_id = @pull_request_id
		  AND status = 'completed' AND decision IS NOT NULL`, pgx.NamedArgs{
		"org_id": orgID, "pull_request_id": pullRequestID,
	})
	if err != nil {
		return fmt.Errorf("list pull request decisions for outcome projection: %w", err)
	}
	sessionIDs, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return fmt.Errorf("collect pull request decisions for outcome projection: %w", err)
	}
	return s.projectDecisions(ctx, s.db, orgID, sessionIDs)
}

func (s *CodeReviewInsightStore) recordPullRequestLifecycle(ctx context.Context, orgID, pullRequestID uuid.UUID, merged bool, mergedAt *time.Time, terminal bool, observedAt time.Time) (bool, error) {
	result, err := s.db.Exec(ctx, `WITH lifecycle AS (
		INSERT INTO code_review_pull_request_lifecycle_observations (
			org_id, pull_request_id, merged, merged_at, terminal, observed_at
		) VALUES (@org_id, @pull_request_id, @merged, @merged_at, @terminal, @observed_at)
		ON CONFLICT (org_id, pull_request_id) DO UPDATE SET
			merged = EXCLUDED.merged, merged_at = EXCLUDED.merged_at,
			terminal = EXCLUDED.terminal, observed_at = EXCLUDED.observed_at
		WHERE code_review_pull_request_lifecycle_observations.observed_at <= EXCLUDED.observed_at
		RETURNING merged, merged_at, terminal
	)
	UPDATE pull_requests pull_request SET
		status = CASE
			WHEN lifecycle.terminal AND lifecycle.merged THEN @merged_status
			WHEN lifecycle.terminal THEN @closed_status
			ELSE @open_status
		END,
		merged_at = CASE WHEN lifecycle.merged THEN lifecycle.merged_at ELSE NULL END,
		updated_at = now()
	FROM lifecycle
	WHERE pull_request.org_id = @org_id AND pull_request.id = @pull_request_id`, pgx.NamedArgs{
		"org_id": orgID, "pull_request_id": pullRequestID, "merged": merged,
		"merged_at": mergedAt, "terminal": terminal, "observed_at": observedAt,
		"merged_status": models.PullRequestStatusMerged, "closed_status": models.PullRequestStatusClosed,
		"open_status": models.PullRequestStatusOpen,
	})
	if err != nil {
		return false, fmt.Errorf("record pull request lifecycle observation: %w", err)
	}
	return result.RowsAffected() > 0, nil
}

func lockCodeReviewOutcomeRows(ctx context.Context, tx pgx.Tx, orgID, pullRequestID uuid.UUID) ([]time.Time, error) {
	rows, err := tx.Query(ctx, `SELECT observed_until
		FROM code_review_decision_outcomes
		WHERE org_id = @org_id AND pull_request_id = @pull_request_id
		ORDER BY session_id
		FOR UPDATE`, pgx.NamedArgs{"org_id": orgID, "pull_request_id": pullRequestID})
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[time.Time])
}

// RecordHumanReview records the exact provider review identity and then derives
// the singular ranking projection from all still-active independent reviews.
func (s *CodeReviewInsightStore) RecordHumanReview(ctx context.Context, orgID, pullRequestID uuid.UUID, reviewID int64, login, reviewerType, authorAssociation, pullRequestAuthor, state string, observedAt time.Time) error {
	login = strings.TrimSpace(login)
	if reviewID <= 0 || login == "" {
		return fmt.Errorf("positive review id and reviewer login are required")
	}
	state = strings.ToLower(strings.TrimSpace(state))
	switch state {
	case "approved", "changes_requested":
	default:
		return fmt.Errorf("unsupported human review state: %q", state)
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	if err := s.projectPullRequestDecisions(ctx, orgID, pullRequestID); err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin human code review observation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := lockCodeReviewOutcomeRows(ctx, tx, orgID, pullRequestID); err != nil {
		return fmt.Errorf("lock code review outcomes for human review observation: %w", err)
	}
	independent := strings.EqualFold(strings.TrimSpace(reviewerType), "User") &&
		codeReviewIndependentAssociation(authorAssociation) &&
		strings.TrimSpace(pullRequestAuthor) != "" &&
		!strings.EqualFold(login, strings.TrimSpace(pullRequestAuthor))
	_, err = tx.Exec(ctx, `INSERT INTO code_review_human_review_observations (
		org_id, pull_request_id, github_review_id, reviewer_login, reviewer_type, author_association,
		state, independent, active, observed_at
	) VALUES (@org_id, @pull_request_id, @review_id, @login, @reviewer_type, @association,
		@state, @independent, true, @observed_at)
	ON CONFLICT (org_id, pull_request_id, github_review_id) DO UPDATE SET
		reviewer_login = CASE WHEN EXCLUDED.observed_at >= code_review_human_review_observations.observed_at THEN EXCLUDED.reviewer_login ELSE code_review_human_review_observations.reviewer_login END,
		reviewer_type = CASE WHEN EXCLUDED.observed_at >= code_review_human_review_observations.observed_at THEN EXCLUDED.reviewer_type ELSE code_review_human_review_observations.reviewer_type END,
		author_association = CASE WHEN EXCLUDED.observed_at >= code_review_human_review_observations.observed_at THEN EXCLUDED.author_association ELSE code_review_human_review_observations.author_association END,
		state = CASE WHEN EXCLUDED.observed_at >= code_review_human_review_observations.observed_at THEN EXCLUDED.state ELSE code_review_human_review_observations.state END,
		independent = CASE WHEN EXCLUDED.observed_at >= code_review_human_review_observations.observed_at THEN EXCLUDED.independent ELSE code_review_human_review_observations.independent END,
		active = CASE
			WHEN EXCLUDED.observed_at >= code_review_human_review_observations.observed_at THEN true
			ELSE code_review_human_review_observations.active
		END,
		observed_at = GREATEST(code_review_human_review_observations.observed_at, EXCLUDED.observed_at)`, pgx.NamedArgs{
		"org_id": orgID, "pull_request_id": pullRequestID, "review_id": reviewID,
		"login": login, "reviewer_type": strings.TrimSpace(reviewerType), "association": strings.ToUpper(strings.TrimSpace(authorAssociation)),
		"state": state, "independent": independent, "observed_at": observedAt,
	})
	if err != nil {
		return fmt.Errorf("record human code review observation: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE code_review_human_review_observations observation SET
		active = observation.active AND observation.github_review_id = (
			SELECT latest.github_review_id FROM code_review_human_review_observations latest
			WHERE latest.org_id = @org_id AND latest.pull_request_id = @pull_request_id
			  AND lower(latest.reviewer_login) = lower(@login)
			ORDER BY latest.observed_at DESC, latest.github_review_id DESC LIMIT 1
		)
		WHERE observation.org_id = @org_id AND observation.pull_request_id = @pull_request_id
		  AND lower(observation.reviewer_login) = lower(@login)`, pgx.NamedArgs{
		"org_id": orgID, "pull_request_id": pullRequestID, "login": login,
	}); err != nil {
		return fmt.Errorf("select latest human code review observation: %w", err)
	}
	if err := s.refreshHumanReviewProjection(ctx, tx, orgID, pullRequestID, observedAt); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit human code review observation: %w", err)
	}
	return nil
}

func codeReviewIndependentAssociation(association string) bool {
	switch strings.ToUpper(strings.TrimSpace(association)) {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	default:
		return false
	}
}

func (s *CodeReviewInsightStore) DismissHumanReview(ctx context.Context, orgID, pullRequestID uuid.UUID, reviewID int64, login, reviewerType, authorAssociation, pullRequestAuthor string, observedAt time.Time) error {
	if reviewID <= 0 {
		return fmt.Errorf("positive review id is required")
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	if err := s.projectPullRequestDecisions(ctx, orgID, pullRequestID); err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin human code review dismissal: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := lockCodeReviewOutcomeRows(ctx, tx, orgID, pullRequestID); err != nil {
		return fmt.Errorf("lock code review outcomes for human review dismissal: %w", err)
	}
	independent := strings.EqualFold(strings.TrimSpace(reviewerType), "User") &&
		codeReviewIndependentAssociation(authorAssociation) &&
		strings.TrimSpace(pullRequestAuthor) != "" &&
		!strings.EqualFold(strings.TrimSpace(login), strings.TrimSpace(pullRequestAuthor))
	_, err = tx.Exec(ctx, `INSERT INTO code_review_human_review_observations (
		org_id, pull_request_id, github_review_id, reviewer_login, reviewer_type, author_association,
		state, independent, active, observed_at
	) VALUES (@org_id, @pull_request_id, @review_id, @login, @reviewer_type, @association,
		'dismissed', @independent, false, @observed_at)
	ON CONFLICT (org_id, pull_request_id, github_review_id) DO UPDATE SET
		reviewer_login = CASE WHEN EXCLUDED.observed_at >= code_review_human_review_observations.observed_at THEN EXCLUDED.reviewer_login ELSE code_review_human_review_observations.reviewer_login END,
		reviewer_type = CASE WHEN EXCLUDED.observed_at >= code_review_human_review_observations.observed_at THEN EXCLUDED.reviewer_type ELSE code_review_human_review_observations.reviewer_type END,
		author_association = CASE WHEN EXCLUDED.observed_at >= code_review_human_review_observations.observed_at THEN EXCLUDED.author_association ELSE code_review_human_review_observations.author_association END,
		state = CASE WHEN EXCLUDED.observed_at >= code_review_human_review_observations.observed_at THEN 'dismissed' ELSE code_review_human_review_observations.state END,
		independent = CASE WHEN EXCLUDED.observed_at >= code_review_human_review_observations.observed_at THEN EXCLUDED.independent ELSE code_review_human_review_observations.independent END,
		active = CASE WHEN EXCLUDED.observed_at >= code_review_human_review_observations.observed_at THEN false ELSE code_review_human_review_observations.active END,
		observed_at = GREATEST(code_review_human_review_observations.observed_at, EXCLUDED.observed_at)`, pgx.NamedArgs{
		"org_id": orgID, "pull_request_id": pullRequestID, "review_id": reviewID,
		"login": strings.TrimSpace(login), "reviewer_type": strings.TrimSpace(reviewerType),
		"association": strings.ToUpper(strings.TrimSpace(authorAssociation)), "independent": independent,
		"observed_at": observedAt,
	})
	if err != nil {
		return fmt.Errorf("dismiss human code review observation: %w", err)
	}
	if err := s.refreshHumanReviewProjection(ctx, tx, orgID, pullRequestID, observedAt); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit human code review dismissal: %w", err)
	}
	return nil
}

// RefreshHumanReviewCommentCount derives the current human-authored review
// comment count after a durable comment insert. Projecting first makes this
// safe when a comment webhook arrives before the decision-completion job.
func (s *CodeReviewInsightStore) RefreshHumanReviewCommentCount(ctx context.Context, orgID, pullRequestID uuid.UUID, observedAt time.Time) error {
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	if err := s.projectPullRequestDecisions(ctx, orgID, pullRequestID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE code_review_decision_outcomes outcome SET
		human_review_comment_count = (
			SELECT count(*)::integer FROM review_comments comment
			WHERE comment.org_id = @org_id AND comment.pull_request_id = @pull_request_id
			  AND lower(comment.reviewer_type) = 'user'
		),
		observed_until = GREATEST(outcome.observed_until, @observed_at),
		projection_updated_at = now()
		WHERE outcome.org_id = @org_id AND outcome.pull_request_id = @pull_request_id`, pgx.NamedArgs{
		"org_id": orgID, "pull_request_id": pullRequestID, "observed_at": observedAt,
	})
	if err != nil {
		return fmt.Errorf("refresh human review comment count: %w", err)
	}
	return nil
}

func (s *CodeReviewInsightStore) refreshHumanReviewProjection(ctx context.Context, q DBTX, orgID, pullRequestID uuid.UUID, observedAt time.Time) error {
	_, err := q.Exec(ctx, `UPDATE code_review_decision_outcomes outcome SET
		independent_approver_login = (
			SELECT reviewer_login FROM code_review_human_review_observations observation
			WHERE observation.org_id = @org_id AND observation.pull_request_id = @pull_request_id
			  AND observation.active = true AND observation.independent = true AND observation.state = 'approved'
			ORDER BY observation.observed_at DESC, observation.github_review_id DESC LIMIT 1
		),
		independent_blocking_review_login = (
			SELECT reviewer_login FROM code_review_human_review_observations observation
			WHERE observation.org_id = @org_id AND observation.pull_request_id = @pull_request_id
			  AND observation.active = true AND observation.independent = true AND observation.state = 'changes_requested'
			ORDER BY observation.observed_at DESC, observation.github_review_id DESC LIMIT 1
		),
		observed_until = GREATEST(outcome.observed_until, @observed_at), projection_updated_at = now()
		WHERE outcome.org_id = @org_id AND outcome.pull_request_id = @pull_request_id`, pgx.NamedArgs{
		"org_id": orgID, "pull_request_id": pullRequestID, "observed_at": observedAt,
	})
	if err != nil {
		return fmt.Errorf("refresh independent human review projection: %w", err)
	}
	return nil
}

func (s *CodeReviewInsightStore) RecordPullRequestTerminal(ctx context.Context, orgID, pullRequestID uuid.UUID, merged bool, mergedAt *time.Time, observedAt time.Time) (bool, error) {
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	accepted, err := s.recordPullRequestLifecycle(ctx, orgID, pullRequestID, merged, mergedAt, true, observedAt)
	if err != nil || !accepted {
		return accepted, err
	}
	if err := s.projectPullRequestDecisions(ctx, orgID, pullRequestID); err != nil {
		return true, err
	}
	return true, nil
}

func (s *CodeReviewInsightStore) RecordPullRequestOpen(ctx context.Context, orgID, pullRequestID uuid.UUID, observedAt time.Time) (bool, error) {
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	accepted, err := s.recordPullRequestLifecycle(ctx, orgID, pullRequestID, false, nil, false, observedAt)
	if err != nil || !accepted {
		return accepted, err
	}
	if err := s.projectPullRequestDecisions(ctx, orgID, pullRequestID); err != nil {
		return true, err
	}
	return true, nil
}

func (s *CodeReviewInsightStore) ListPullRequestsForOutcomeReconciliation(ctx context.Context, orgID uuid.UUID, observedBefore time.Time, limit int) ([]models.CodeReviewOutcomePullRequest, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `SELECT outcome.pull_request_id, outcome.repository_id, pull_request.github_pr_number
		FROM code_review_decision_outcomes outcome
		JOIN pull_requests pull_request
		  ON pull_request.org_id = outcome.org_id AND pull_request.id = outcome.pull_request_id
		WHERE outcome.org_id = @org_id
		  AND outcome.observed_until < @observed_before
		  AND (outcome.terminal = false OR outcome.created_at >= now() - interval '30 days')
		GROUP BY outcome.pull_request_id, outcome.repository_id, pull_request.github_pr_number
		ORDER BY max(outcome.provider_reconcile_attempted_at) NULLS FIRST,
		         min(outcome.observed_until), outcome.pull_request_id
		LIMIT @limit`, pgx.NamedArgs{"org_id": orgID, "observed_before": observedBefore, "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("list pull requests for code review outcome reconciliation: %w", err)
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.CodeReviewOutcomePullRequest])
	if err != nil {
		return nil, fmt.Errorf("collect pull requests for code review outcome reconciliation: %w", err)
	}
	return items, nil
}

func (s *CodeReviewInsightStore) RecordOutcomeReconciliationAttempt(ctx context.Context, orgID, pullRequestID uuid.UUID, attemptedAt time.Time) error {
	_, err := s.db.Exec(ctx, `UPDATE code_review_decision_outcomes
		SET provider_reconcile_attempted_at = @attempted_at
		WHERE org_id = @org_id AND pull_request_id = @pull_request_id`, pgx.NamedArgs{
		"org_id": orgID, "pull_request_id": pullRequestID, "attempted_at": attemptedAt,
	})
	if err != nil {
		return fmt.Errorf("record provider outcome reconciliation attempt: %w", err)
	}
	return nil
}

// ReconcilePullRequestOutcome replaces the provider-derived review set and PR
// lifecycle facts atomically. Missing review IDs become inactive, which repairs
// dropped dismissal webhooks without losing their historical observation rows.
func (s *CodeReviewInsightStore) ReconcilePullRequestOutcome(ctx context.Context, orgID, pullRequestID uuid.UUID, snapshot models.CodeReviewOutcomeSnapshot) error {
	if snapshot.ObservedAt.IsZero() {
		return fmt.Errorf("provider observation time is required")
	}
	terminal := strings.EqualFold(strings.TrimSpace(snapshot.State), "closed")
	if _, err := s.recordPullRequestLifecycle(ctx, orgID, pullRequestID, snapshot.Merged, snapshot.MergedAt, terminal, snapshot.ObservedAt); err != nil {
		return err
	}
	if err := s.projectPullRequestDecisions(ctx, orgID, pullRequestID); err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin pull request outcome reconciliation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Serialize the provider snapshot with webhook projections for this pull
	// request. A webhook that committed after the snapshot started wins; one
	// already in flight completes after this transaction and wins as well.
	watermarks, err := lockCodeReviewOutcomeRows(ctx, tx, orgID, pullRequestID)
	if err != nil {
		return fmt.Errorf("lock code review outcomes for provider reconciliation: %w", err)
	}
	for _, watermark := range watermarks {
		if watermark.After(snapshot.ObservedAt) {
			return nil
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE code_review_human_review_observations SET active = false
		WHERE org_id = @org_id AND pull_request_id = @pull_request_id
		  AND observed_at <= @observed_at`, pgx.NamedArgs{
		"org_id": orgID, "pull_request_id": pullRequestID, "observed_at": snapshot.ObservedAt,
	}); err != nil {
		return fmt.Errorf("retire provider review observations: %w", err)
	}
	upserts := make([]codeReviewObservationUpsert, 0, len(snapshot.Reviews))
	for _, review := range latestCodeReviewObservationsByReviewer(snapshot.Reviews) {
		if review.State != "approved" && review.State != "changes_requested" {
			continue
		}
		independent := strings.EqualFold(strings.TrimSpace(review.ReviewerType), "User") &&
			codeReviewIndependentAssociation(review.AuthorAssociation) &&
			strings.TrimSpace(snapshot.AuthorLogin) != "" &&
			!strings.EqualFold(review.ReviewerLogin, strings.TrimSpace(snapshot.AuthorLogin))
		observedAt := review.SubmittedAt
		if observedAt.IsZero() {
			observedAt = snapshot.ObservedAt
		}
		upserts = append(upserts, codeReviewObservationUpsert{
			ReviewID: review.GitHubReviewID, Login: strings.TrimSpace(review.ReviewerLogin),
			ReviewerType:      strings.TrimSpace(review.ReviewerType),
			AuthorAssociation: strings.ToUpper(strings.TrimSpace(review.AuthorAssociation)),
			State:             review.State, Independent: independent, ObservedAt: observedAt,
		})
	}
	if len(upserts) > 0 {
		encoded, err := json.Marshal(upserts)
		if err != nil {
			return fmt.Errorf("encode provider review observations: %w", err)
		}
		if _, err := tx.Exec(ctx, `WITH observations AS (
			SELECT review_id, login, reviewer_type, author_association, state, independent, observed_at
			FROM jsonb_to_recordset(@observations::jsonb) AS observation(
				review_id bigint, login text, reviewer_type text, author_association text,
				state text, independent boolean, observed_at timestamptz
			)
		)
		INSERT INTO code_review_human_review_observations (
			org_id, pull_request_id, github_review_id, reviewer_login, reviewer_type, author_association,
			state, independent, active, observed_at
		)
		SELECT @org_id, @pull_request_id, review_id, login, reviewer_type, author_association,
			state, independent, true, observed_at FROM observations
		ON CONFLICT (org_id, pull_request_id, github_review_id) DO UPDATE SET
			reviewer_login = CASE WHEN EXCLUDED.observed_at >= code_review_human_review_observations.observed_at THEN EXCLUDED.reviewer_login ELSE code_review_human_review_observations.reviewer_login END,
			reviewer_type = CASE WHEN EXCLUDED.observed_at >= code_review_human_review_observations.observed_at THEN EXCLUDED.reviewer_type ELSE code_review_human_review_observations.reviewer_type END,
			author_association = CASE WHEN EXCLUDED.observed_at >= code_review_human_review_observations.observed_at THEN EXCLUDED.author_association ELSE code_review_human_review_observations.author_association END,
			state = CASE WHEN EXCLUDED.observed_at >= code_review_human_review_observations.observed_at THEN EXCLUDED.state ELSE code_review_human_review_observations.state END,
			independent = CASE WHEN EXCLUDED.observed_at >= code_review_human_review_observations.observed_at THEN EXCLUDED.independent ELSE code_review_human_review_observations.independent END,
			active = CASE WHEN EXCLUDED.observed_at >= code_review_human_review_observations.observed_at THEN true ELSE code_review_human_review_observations.active END,
			observed_at = GREATEST(code_review_human_review_observations.observed_at, EXCLUDED.observed_at)`, pgx.NamedArgs{
			"org_id": orgID, "pull_request_id": pullRequestID, "observations": string(encoded),
		}); err != nil {
			return fmt.Errorf("reconcile provider review observations: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE code_review_decision_outcomes SET
		merged = @merged, merged_at = @merged_at, terminal = @terminal,
		observed_until = @observed_at,
		lifecycle_observed_at = @observed_at,
		provider_reconcile_attempted_at = @observed_at, projection_updated_at = now()
		WHERE org_id = @org_id AND pull_request_id = @pull_request_id
		  AND (lifecycle_observed_at IS NULL OR lifecycle_observed_at <= @observed_at)`, pgx.NamedArgs{
		"org_id": orgID, "pull_request_id": pullRequestID, "merged": snapshot.Merged,
		"merged_at": snapshot.MergedAt, "terminal": terminal, "observed_at": snapshot.ObservedAt,
	}); err != nil {
		return fmt.Errorf("reconcile provider pull request outcome: %w", err)
	}
	if err := s.refreshHumanReviewProjection(ctx, tx, orgID, pullRequestID, snapshot.ObservedAt); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit pull request outcome reconciliation: %w", err)
	}
	return nil
}

func latestCodeReviewObservationsByReviewer(reviews []models.CodeReviewHumanReviewObservation) []models.CodeReviewHumanReviewObservation {
	latestByReviewer := make(map[string]models.CodeReviewHumanReviewObservation)
	for _, review := range reviews {
		login := strings.TrimSpace(review.ReviewerLogin)
		if review.GitHubReviewID <= 0 || login == "" {
			continue
		}
		review.ReviewerLogin = login
		review.State = strings.ToLower(strings.TrimSpace(review.State))
		if review.State != "approved" && review.State != "changes_requested" && review.State != "dismissed" {
			continue
		}
		key := strings.ToLower(login)
		previous, exists := latestByReviewer[key]
		if !exists || review.SubmittedAt.After(previous.SubmittedAt) ||
			(review.SubmittedAt.Equal(previous.SubmittedAt) && review.GitHubReviewID > previous.GitHubReviewID) {
			latestByReviewer[key] = review
		}
	}
	latest := make([]models.CodeReviewHumanReviewObservation, 0, len(latestByReviewer))
	for _, review := range latestByReviewer {
		latest = append(latest, review)
	}
	return latest
}

type codeReviewObservationUpsert struct {
	ReviewID          int64     `json:"review_id"`
	Login             string    `json:"login"`
	ReviewerType      string    `json:"reviewer_type"`
	AuthorAssociation string    `json:"author_association"`
	State             string    `json:"state"`
	Independent       bool      `json:"independent"`
	ObservedAt        time.Time `json:"observed_at"`
}

func (s *CodeReviewInsightStore) GetOutcome(ctx context.Context, orgID, sessionID uuid.UUID) (models.CodeReviewDecisionOutcome, error) {
	rows, err := s.db.Query(ctx, `SELECT `+codeReviewDecisionOutcomeColumns+`
		FROM code_review_decision_outcomes WHERE org_id = @org_id AND session_id = @session_id`,
		pgx.NamedArgs{"org_id": orgID, "session_id": sessionID})
	if err != nil {
		return models.CodeReviewDecisionOutcome{}, fmt.Errorf("query code review decision outcome: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CodeReviewDecisionOutcome])
}

// RankingEnabled enforces the product trigger: at least ten eligible disputes
// in each of the two preceding complete UTC calendar months.
func (s *CodeReviewInsightStore) RankingEnabled(ctx context.Context, orgID uuid.UUID) (bool, error) {
	var enabled bool
	err := s.db.QueryRow(ctx, `WITH month_counts AS (
		SELECT date_trunc('month', created_at) AS month, count(*) AS disputes
		FROM code_review_decision_disputes
		WHERE org_id = @org_id AND adjudication_status IS NOT NULL
		  AND adjudication_status <> 'expired' AND superseded_by_dispute_id IS NULL
		  AND created_at >= date_trunc('month', now()) - interval '2 months'
		  AND created_at < date_trunc('month', now())
		GROUP BY date_trunc('month', created_at)
	)
	SELECT count(*) = 2 AND min(disputes) >= 10 FROM month_counts`, pgx.NamedArgs{"org_id": orgID}).Scan(&enabled)
	if err != nil {
		return false, fmt.Errorf("check code review dispute ranking volume: %w", err)
	}
	return enabled, nil
}

type CodeReviewRankCandidate struct {
	Dispute           models.CodeReviewDispute
	Outcome           *models.CodeReviewDecisionOutcome
	RepeatReasonCount int
	BasePolicyActive  bool
}

type codeReviewRankRow struct {
	ID                             uuid.UUID                                  `db:"id"`
	Decision                       models.CodeReviewDecision                  `db:"decision"`
	CreatedAt                      time.Time                                  `db:"created_at"`
	ReassessmentStatus             models.CodeReviewDisputeReassessmentStatus `db:"reassessment_status"`
	ReassessmentFlipped            *bool                                      `db:"reassessment_flipped"`
	AuthorIsPRAuthor               bool                                       `db:"author_is_pr_author"`
	EscalatedAt                    *time.Time                                 `db:"escalated_at"`
	OutcomeObservedUntil           *time.Time                                 `db:"outcome_observed_until"`
	IndependentApproverLogin       *string                                    `db:"independent_approver_login"`
	IndependentBlockingReviewLogin *string                                    `db:"independent_blocking_review_login"`
	RepeatReasonCount              int                                        `db:"repeat_reason_count"`
	BasePolicyActive               bool                                       `db:"base_policy_active"`
}

func (s *CodeReviewInsightStore) ListPendingRankCandidates(ctx context.Context, orgID uuid.UUID, limit int, rankingEnabled bool) ([]CodeReviewRankCandidate, error) {
	if limit <= 0 || limit > 1000 {
		limit = 250
	}
	rows, err := s.db.Query(ctx, `SELECT d.id, d.decision, d.created_at,
			d.reassessment_status, d.reassessment_flipped, d.author_is_pr_author, d.escalated_at,
			o.observed_until AS outcome_observed_until,
			o.independent_approver_login, o.independent_blocking_review_login,
			repeats.repeat_reason_count,
			COALESCE(p.active, false) AS base_policy_active
		FROM code_review_decision_disputes d
		LEFT JOIN code_review_decision_outcomes o
		  ON o.org_id = d.org_id AND o.session_id = d.session_id
		LEFT JOIN code_review_policies p
		  ON p.org_id = d.org_id AND p.id = d.policy_id
		CROSS JOIN LATERAL (
			SELECT count(*)::integer AS repeat_reason_count
			 FROM code_review_decision_disputes other
			 WHERE other.org_id = d.org_id AND other.repository_id = d.repository_id
			   AND other.id <> d.id AND other.created_at >= now() - interval '14 days'
			   AND other.created_at <= now() AND other.adjudication_status IS NOT NULL
				   AND other.adjudication_status <> 'expired' AND other.superseded_by_dispute_id IS NULL
				   AND other.contested_reason_codes && d.contested_reason_codes
		) repeats
		WHERE d.org_id = @org_id AND d.adjudication_status = 'pending'
		  AND (
			NOT (d.queue_signals ? 'computed_at')
			OR NULLIF(d.queue_signals->>'computed_at', '')::timestamptz < now() - interval '1 day'
			OR NULLIF(d.queue_signals->>'computed_at', '')::timestamptz < d.updated_at
			OR NULLIF(d.queue_signals->>'computed_at', '')::timestamptz < o.projection_updated_at
			OR COALESCE(NULLIF(d.queue_signals->>'repeat_reason_disputes_14_days', '')::integer, -1) IS DISTINCT FROM repeats.repeat_reason_count
			OR COALESCE((d.queue_signals->>'ranking_enabled')::boolean, false) IS DISTINCT FROM @ranking_enabled
			OR COALESCE((d.queue_signals->>'base_policy_superseded')::boolean, false) IS DISTINCT FROM NOT COALESCE(p.active, false)
		  )
		ORDER BY d.updated_at, d.id LIMIT @limit`, pgx.NamedArgs{
		"org_id": orgID, "limit": limit, "ranking_enabled": rankingEnabled,
	})
	if err != nil {
		return nil, fmt.Errorf("list pending code review dispute rank candidates: %w", err)
	}
	rankRows, err := pgx.CollectRows(rows, pgx.RowToStructByName[codeReviewRankRow])
	if err != nil {
		return nil, fmt.Errorf("collect pending code review dispute rank candidates: %w", err)
	}
	candidates := make([]CodeReviewRankCandidate, 0, len(rankRows))
	for _, row := range rankRows {
		candidate := CodeReviewRankCandidate{
			Dispute: models.CodeReviewDispute{
				ID: row.ID, Decision: row.Decision, CreatedAt: row.CreatedAt,
				ReassessmentStatus: row.ReassessmentStatus, ReassessmentFlipped: row.ReassessmentFlipped,
				AuthorIsPRAuthor: row.AuthorIsPRAuthor, EscalatedAt: row.EscalatedAt,
			},
			RepeatReasonCount: row.RepeatReasonCount,
			BasePolicyActive:  row.BasePolicyActive,
		}
		if row.OutcomeObservedUntil != nil {
			candidate.Outcome = &models.CodeReviewDecisionOutcome{
				ObservedUntil:                  *row.OutcomeObservedUntil,
				IndependentApproverLogin:       row.IndependentApproverLogin,
				IndependentBlockingReviewLogin: row.IndependentBlockingReviewLogin,
			}
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

func (s *CodeReviewInsightStore) UpdateDisputeRanks(ctx context.Context, orgID uuid.UUID, updates []models.CodeReviewRankUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	encoded, err := json.Marshal(updates)
	if err != nil {
		return fmt.Errorf("encode code review rank updates: %w", err)
	}
	_, err = s.db.Exec(ctx, `WITH rank_updates AS (
		SELECT id, signals, priority
		FROM jsonb_to_recordset(@updates::jsonb)
			AS update_row(id uuid, signals jsonb, priority double precision)
	)
	UPDATE code_review_decision_disputes dispute
	SET queue_signals = (dispute.queue_signals - ARRAY[
		'independent_human_contradiction', 'independent_human_login',
		'reassessment_unchanged', 'reassessment_flipped', 'filer_is_not_pr_author',
		'repeat_reason_disputes_14_days', 'escalated', 'base_policy_superseded',
		'outcome_fresh', 'ranking_enabled', 'computed_at'
	]::text[]) || rank_updates.signals,
		queue_priority = rank_updates.priority
	FROM rank_updates
	WHERE dispute.org_id = @org_id AND dispute.id = rank_updates.id
	  AND dispute.adjudication_status = 'pending'`, pgx.NamedArgs{
		"org_id": orgID, "updates": string(encoded),
	})
	if err != nil {
		return fmt.Errorf("update code review dispute ranks: %w", err)
	}
	return nil
}
