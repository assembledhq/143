package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const codeReviewDisputeColumns = `id, org_id, session_id, pull_request_id, repository_id, policy_id,
	reviewed_head_sha, decision, direction, filed_by_user_id, filed_by_login, author_association,
	author_is_pr_author, repository_visibility, membership_evidence, trust_override, source,
	github_comment_id, github_thread_root_comment_id, reply_comment_id, source_body_hash, source_version,
	body, contested_reason_codes, dispute_kind, asserts_new_information, routing, intake_status,
	intake_confidence, reassessment_session_id, reassessment_decision, reassessment_flipped,
	reassessment_status, semantic_input_hash_at_filing, semantic_input_hash_at_rerun,
	adjudication_status, adjudicated_by_user_id, adjudicated_at, adjudication_note, escalated_at,
	escalated_by_user_id, queue_signals, queue_priority, reply_status, reply_cycle_reserved, status_detail, version,
	created_at, updated_at`

var ErrCodeReviewDisputeVersionConflict = errors.New("code review dispute version conflict")

type CodeReviewDisputeStore struct {
	db   TxStarter
	jobs *JobStore
}

func NewCodeReviewDisputeStore(db TxStarter) *CodeReviewDisputeStore {
	return &CodeReviewDisputeStore{db: db}
}

func nilIfZeroTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

// SetJobStore wires atomic dispute/job creation.
// lint:allow-no-orgid reason="process-wide dependency injection for the durable job queue"
func (s *CodeReviewDisputeStore) SetJobStore(jobs *JobStore) { s.jobs = jobs }

func (s *CodeReviewDisputeStore) CreateAndEnqueueTriage(ctx context.Context, dispute *models.CodeReviewDispute) (bool, error) {
	if dispute == nil || dispute.OrgID == uuid.Nil || dispute.SessionID == uuid.Nil || dispute.PullRequestID == uuid.Nil || dispute.RepositoryID == uuid.Nil || dispute.PolicyID == uuid.Nil {
		return false, fmt.Errorf("org, session, pull request, repository, and policy are required")
	}
	if err := dispute.RepositoryVisibility.Validate(); err != nil {
		return false, err
	}
	if s.jobs == nil {
		return false, fmt.Errorf("job store is required")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin code review dispute create: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	created, err := insertCodeReviewDispute(ctx, tx, dispute)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) && dispute.GitHubCommentID != nil {
			existing, getErr := getCodeReviewDisputeByGitHubSource(ctx, tx, dispute.OrgID, *dispute.GitHubCommentID, dispute.SourceVersion)
			if getErr != nil {
				return false, errors.Join(fmt.Errorf("dedupe GitHub dispute source: %w", err), getErr)
			}
			*dispute = existing
			return false, nil
		}
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE pull_requests
		SET code_review_dispute_epoch = code_review_dispute_epoch + 1,
		    code_review_dispute_cycles_in_epoch = 0
		WHERE org_id = @org_id AND id = @pull_request_id`, pgx.NamedArgs{
		"org_id": created.OrgID, "pull_request_id": created.PullRequestID,
	}); err != nil {
		return false, fmt.Errorf("reset code review dispute bot-loop epoch: %w", err)
	}
	dedupeKey := "triage_code_review_dispute:" + created.ID.String()
	jobID, err := s.jobs.EnqueueInTxWithOpts(ctx, tx, created.OrgID, EnqueueOpts{
		Queue: "feedback", JobType: models.JobTypeTriageCodeReviewDispute,
		Payload:  map[string]any{"org_id": created.OrgID, "dispute_id": created.ID},
		Priority: 5, DedupeKey: &dedupeKey, MaxAttempts: 5,
	})
	if err != nil {
		return false, fmt.Errorf("enqueue code review dispute triage: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit code review dispute create: %w", err)
	}
	*dispute = created
	s.jobs.Notify(ctx, jobID)
	return true, nil
}

func insertCodeReviewDispute(ctx context.Context, db DBTX, dispute *models.CodeReviewDispute) (models.CodeReviewDispute, error) {
	queueSignals := dispute.QueueSignals
	if len(queueSignals) == 0 {
		queueSignals = json.RawMessage(`{}`)
	}
	rows, err := db.Query(ctx, `
		INSERT INTO code_review_decision_disputes (
			org_id, session_id, pull_request_id, repository_id, policy_id, reviewed_head_sha,
			decision, direction, filed_by_user_id, filed_by_login, author_association, author_is_pr_author,
			repository_visibility, membership_evidence, source, github_comment_id,
			github_thread_root_comment_id, source_body_hash, source_version, body,
			contested_reason_codes, semantic_input_hash_at_filing, queue_signals, reply_status
		) SELECT
			@org_id, @session_id, @pull_request_id, @repository_id, @policy_id, @reviewed_head_sha,
			@decision, @direction, @filed_by_user_id, @filed_by_login, @author_association, @author_is_pr_author,
			@repository_visibility, @membership_evidence, @source, @github_comment_id,
			@github_thread_root_comment_id, @source_body_hash, @source_version, @body,
			@contested_reason_codes, @semantic_input_hash_at_filing, @queue_signals, @reply_status
		FROM code_review_session_metadata reviewed
		JOIN sessions review_session ON review_session.id = reviewed.session_id AND review_session.org_id = @org_id
		JOIN pull_requests pull_request ON pull_request.id = reviewed.pull_request_id AND pull_request.org_id = @org_id
		JOIN repositories repository ON repository.id = reviewed.repository_id AND repository.org_id = @org_id
		JOIN code_review_policies policy ON policy.id = reviewed.policy_id AND policy.org_id = @org_id
		WHERE reviewed.org_id = @org_id AND reviewed.session_id = @session_id
		  AND reviewed.pull_request_id = @pull_request_id AND reviewed.repository_id = @repository_id
		  AND reviewed.policy_id = @policy_id AND reviewed.head_sha = @reviewed_head_sha
		  AND reviewed.status = 'completed' AND reviewed.decision = @decision
		ON CONFLICT (org_id, github_comment_id, source_version)
			WHERE github_comment_id IS NOT NULL
			DO NOTHING
		RETURNING `+codeReviewDisputeColumns, pgx.NamedArgs{
		"org_id": dispute.OrgID, "session_id": dispute.SessionID, "pull_request_id": dispute.PullRequestID,
		"repository_id": dispute.RepositoryID, "policy_id": dispute.PolicyID,
		"reviewed_head_sha": dispute.ReviewedHeadSHA, "decision": dispute.Decision, "direction": dispute.Direction,
		"filed_by_user_id": dispute.FiledByUserID, "filed_by_login": strings.TrimSpace(dispute.FiledByLogin),
		"author_association":  strings.ToUpper(strings.TrimSpace(dispute.AuthorAssociation)),
		"author_is_pr_author": dispute.AuthorIsPRAuthor, "repository_visibility": dispute.RepositoryVisibility,
		"membership_evidence": dispute.MembershipEvidence, "source": dispute.Source,
		"github_comment_id": dispute.GitHubCommentID, "github_thread_root_comment_id": dispute.GitHubThreadRootCommentID,
		"source_body_hash": dispute.SourceBodyHash, "source_version": dispute.SourceVersion,
		"body": strings.TrimSpace(dispute.Body), "contested_reason_codes": dispute.ContestedReasonCodes,
		"semantic_input_hash_at_filing": dispute.SemanticInputHashAtFiling, "queue_signals": queueSignals,
		"reply_status": dispute.ReplyStatus,
	})
	if err != nil {
		return models.CodeReviewDispute{}, fmt.Errorf("insert code review dispute: %w", err)
	}
	created, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CodeReviewDispute])
	if err != nil {
		return models.CodeReviewDispute{}, fmt.Errorf("collect code review dispute: %w", err)
	}
	return created, nil
}

func getCodeReviewDisputeByGitHubSource(ctx context.Context, db DBTX, orgID uuid.UUID, commentID, version int64) (models.CodeReviewDispute, error) {
	rows, err := db.Query(ctx, `SELECT `+codeReviewDisputeColumns+`
		FROM code_review_decision_disputes
		WHERE org_id = @org_id AND github_comment_id = @comment_id AND source_version = @source_version`, pgx.NamedArgs{
		"org_id": orgID, "comment_id": commentID, "source_version": version,
	})
	if err != nil {
		return models.CodeReviewDispute{}, fmt.Errorf("query GitHub code review dispute: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CodeReviewDispute])
}

func (s *CodeReviewDisputeStore) GetByID(ctx context.Context, orgID, disputeID uuid.UUID) (models.CodeReviewDispute, error) {
	rows, err := s.db.Query(ctx, `SELECT `+codeReviewDisputeColumns+`
		FROM code_review_decision_disputes WHERE org_id = @org_id AND id = @id`, pgx.NamedArgs{"org_id": orgID, "id": disputeID})
	if err != nil {
		return models.CodeReviewDispute{}, fmt.Errorf("query code review dispute: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CodeReviewDispute])
}

func (s *CodeReviewDisputeStore) ListBySession(ctx context.Context, orgID, sessionID uuid.UUID, cursor *uuid.UUID, limit int) (models.CodeReviewDisputePage, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	args := pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "limit": limit + 1}
	cursorSQL := ""
	if cursor != nil {
		cursorSQL = ` AND (created_at, id) < (SELECT created_at, id FROM code_review_decision_disputes WHERE org_id = @org_id AND id = @cursor)`
		args["cursor"] = *cursor
	}
	rows, err := s.db.Query(ctx, `SELECT `+codeReviewDisputeColumns+`
		FROM code_review_decision_disputes
		WHERE org_id = @org_id AND session_id = @session_id`+cursorSQL+`
		ORDER BY created_at DESC, id DESC LIMIT @limit`, args)
	if err != nil {
		return models.CodeReviewDisputePage{}, fmt.Errorf("list session code review disputes: %w", err)
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.CodeReviewDispute])
	if err != nil {
		return models.CodeReviewDisputePage{}, err
	}
	return codeReviewDisputePage(items, limit), nil
}

func (s *CodeReviewDisputeStore) ListQueue(ctx context.Context, orgID uuid.UUID, filters models.CodeReviewDisputeListFilters) (models.CodeReviewDisputePage, error) {
	limit := filters.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	args := pgx.NamedArgs{"org_id": orgID, "limit": limit + 1}
	where := ` WHERE org_id = @org_id AND adjudication_status IS NOT NULL`
	if filters.AdjudicationStatus != nil {
		where += ` AND adjudication_status = @adjudication_status`
		args["adjudication_status"] = *filters.AdjudicationStatus
	}
	if filters.RepositoryID != nil {
		where += ` AND repository_id = @repository_id`
		args["repository_id"] = *filters.RepositoryID
	}
	if filters.Direction != nil {
		where += ` AND direction = @direction`
		args["direction"] = *filters.Direction
	}
	if filters.Cursor != nil {
		where += ` AND (queue_priority, id) < (SELECT queue_priority, id FROM code_review_decision_disputes WHERE org_id = @org_id AND id = @cursor)`
		args["cursor"] = *filters.Cursor
	}
	rows, err := s.db.Query(ctx, `SELECT `+codeReviewDisputeColumns+` FROM code_review_decision_disputes`+where+`
		ORDER BY queue_priority DESC, id DESC LIMIT @limit`, args)
	if err != nil {
		return models.CodeReviewDisputePage{}, fmt.Errorf("list code review dispute queue: %w", err)
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.CodeReviewDispute])
	if err != nil {
		return models.CodeReviewDisputePage{}, err
	}
	return codeReviewDisputePage(items, limit), nil
}

func (s *CodeReviewDisputeStore) ListRecentKinds(ctx context.Context, orgID uuid.UUID, limit int) ([]string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `SELECT dispute_kind
		FROM code_review_decision_disputes
		WHERE org_id = @org_id AND dispute_kind IS NOT NULL
		GROUP BY dispute_kind
		ORDER BY max(updated_at) DESC, dispute_kind
		LIMIT @limit`, pgx.NamedArgs{"org_id": orgID, "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("list recent code review dispute kinds: %w", err)
	}
	kinds, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("collect recent code review dispute kinds: %w", err)
	}
	return kinds, nil
}

// CountActiveReassessments supports the operator-wide emergency ceiling.
// lint:allow-no-orgid reason="platform-wide emergency ceiling across all tenant reassessments"
func (s *CodeReviewDisputeStore) CountActiveReassessments(ctx context.Context) (int, error) {
	var count int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM code_review_decision_disputes
		WHERE reassessment_status IN ('queued', 'running')`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count active code review dispute reassessments: %w", err)
	}
	return count, nil
}

func codeReviewDisputePage(items []models.CodeReviewDispute, limit int) models.CodeReviewDisputePage {
	page := models.CodeReviewDisputePage{Items: items}
	if len(items) > limit {
		cursor := items[limit-1].ID
		page.Items = items[:limit]
		page.NextCursor = &cursor
	}
	if page.Items == nil {
		page.Items = []models.CodeReviewDispute{}
	}
	return page
}

func (s *CodeReviewDisputeStore) SetTriage(ctx context.Context, orgID, disputeID uuid.UUID, result models.CodeReviewDisputeTriageResult, adjudicationEligible bool, detail string) (models.CodeReviewDispute, error) {
	status := any(nil)
	if adjudicationEligible {
		status = models.CodeReviewDisputeAdjudicationPending
	}
	intakeStatus := models.CodeReviewDisputeIntakeTriaged
	if result.Routing == models.CodeReviewDisputeRoutingNotADispute {
		intakeStatus = models.CodeReviewDisputeIntakeDiscarded
	}
	rows, err := s.db.Query(ctx, `UPDATE code_review_decision_disputes
		SET direction = @direction, contested_reason_codes = @reason_codes, dispute_kind = @dispute_kind,
		    asserts_new_information = @asserts_new_information, routing = @routing,
		    intake_status = @intake_status, intake_confidence = @confidence,
		    adjudication_status = @adjudication_status, status_detail = NULLIF(@status_detail, ''),
		    updated_at = now(), version = version + 1
		WHERE org_id = @org_id AND id = @id AND intake_status = 'pending'
		RETURNING `+codeReviewDisputeColumns, pgx.NamedArgs{
		"org_id": orgID, "id": disputeID, "direction": result.Direction,
		"reason_codes": result.ContestedReasonCodes, "dispute_kind": normalizeCodeReviewDisputeKind(result.DisputeKind),
		"asserts_new_information": result.AssertsNewInformation, "routing": result.Routing,
		"intake_status": intakeStatus, "confidence": result.Confidence,
		"adjudication_status": status, "status_detail": strings.TrimSpace(detail),
	})
	if err != nil {
		return models.CodeReviewDispute{}, fmt.Errorf("set code review dispute triage: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CodeReviewDispute])
}

func normalizeCodeReviewDisputeKind(value string) any {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.Join(strings.FieldsFunc(value, func(r rune) bool { return r == ' ' || r == '-' || r == '/' }), "_")
	if value == "" {
		return nil
	}
	if runes := []rune(value); len(runes) > 80 {
		value = string(runes[:80])
	}
	return value
}

func (s *CodeReviewDisputeStore) FailTriage(ctx context.Context, orgID, disputeID uuid.UUID, detail string) error {
	_, err := s.db.Exec(ctx, `UPDATE code_review_decision_disputes
		SET intake_status = 'failed', reply_status = CASE WHEN source = 'github_comment' THEN 'pending' ELSE 'not_applicable' END,
		    status_detail = @detail, updated_at = now(), version = version + 1
		WHERE org_id = @org_id AND id = @id AND intake_status = 'pending'`, pgx.NamedArgs{"org_id": orgID, "id": disputeID, "detail": strings.TrimSpace(detail)})
	if err != nil {
		return fmt.Errorf("fail code review dispute triage: %w", err)
	}
	return nil
}

func (s *CodeReviewDisputeStore) RecordAuthorization(ctx context.Context, authorization models.CodeReviewDisputeAuthorization) error {
	if err := authorization.Action.Validate(); err != nil {
		return err
	}
	inputs := authorization.ObservedInputs
	if len(inputs) == 0 {
		inputs = json.RawMessage(`{}`)
	}
	_, err := s.db.Exec(ctx, `INSERT INTO code_review_dispute_authorizations
		(org_id, dispute_id, action, trusted, observed_inputs, policy_version, evaluator_version,
		 override_value, override_by_user_id, decision_reason, decided_at)
		SELECT @org_id, @dispute_id, @action, @trusted, @observed_inputs, COALESCE(@policy_version, policy.version), @evaluator_version,
			 @override_value, @override_by_user_id, @decision_reason, COALESCE(@decided_at, now())
		FROM code_review_decision_disputes dispute
		JOIN code_review_policies policy ON policy.org_id = dispute.org_id AND policy.id = dispute.policy_id
		WHERE dispute.org_id = @org_id AND dispute.id = @dispute_id
		ON CONFLICT DO NOTHING`, pgx.NamedArgs{
		"org_id": authorization.OrgID, "dispute_id": authorization.DisputeID, "action": authorization.Action,
		"trusted": authorization.Trusted, "observed_inputs": inputs, "policy_version": authorization.PolicyVersion,
		"evaluator_version": authorization.EvaluatorVersion, "override_value": authorization.OverrideValue,
		"override_by_user_id": authorization.OverrideByUserID, "decision_reason": authorization.DecisionReason,
		"decided_at": nilIfZeroTime(authorization.DecidedAt),
	})
	if err != nil {
		return fmt.Errorf("record code review dispute authorization: %w", err)
	}
	return nil
}

func (s *CodeReviewDisputeStore) AdmitAndEnqueueReassessment(ctx context.Context, dispute models.CodeReviewDispute, userID *uuid.UUID, semanticHash string, cooldown time.Duration, maxActive int, payload any) (bool, error) {
	if s.jobs == nil {
		return false, fmt.Errorf("job store is required")
	}
	semanticHash = strings.TrimSpace(semanticHash)
	if semanticHash == "" {
		return false, fmt.Errorf("semantic reassessment hash is required")
	}
	if cooldown <= 0 {
		cooldown = 15 * time.Minute
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin reassessment admission: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The operator ceiling and rolling semantic cooldown must be checked in the
	// same serialization domain as admission. Dispute volume is low, so a single
	// platform-wide transaction advisory lock is intentionally simpler and safer
	// than a counter reservation lifecycle that could leak capacity on failure.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(143121)`); err != nil {
		return false, fmt.Errorf("lock reassessment admission: %w", err)
	}
	var existingStatus string
	err = tx.QueryRow(ctx, `SELECT status
		FROM code_review_reassessment_admissions
		WHERE org_id = @org_id AND dispute_id = @dispute_id`, pgx.NamedArgs{
		"org_id": dispute.OrgID, "dispute_id": dispute.ID,
	}).Scan(&existingStatus)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit existing reassessment admission: %w", err)
		}
		return existingStatus == "admitted", nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("load existing reassessment admission: %w", err)
	}

	var equivalentAdmissionID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id
		FROM code_review_reassessment_admissions
		WHERE org_id = @org_id
		  AND pull_request_id = @pull_request_id
		  AND semantic_input_hash = @semantic_hash
		  AND status = 'admitted'
		  AND created_at >= now() - make_interval(secs => @cooldown_seconds)
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, pgx.NamedArgs{
		"org_id": dispute.OrgID, "pull_request_id": dispute.PullRequestID,
		"semantic_hash": semanticHash, "cooldown_seconds": int64(cooldown / time.Second),
	}).Scan(&equivalentAdmissionID)
	if err == nil {
		if _, err := tx.Exec(ctx, `INSERT INTO code_review_reassessment_admissions
			(org_id, dispute_id, pull_request_id, repository_id, user_id, semantic_input_hash, status, denial_reason)
			VALUES (@org_id, @dispute_id, @pull_request_id, @repository_id, @user_id, @semantic_hash, 'deduped', 'equivalent_request_in_cooldown')`, pgx.NamedArgs{
			"org_id": dispute.OrgID, "dispute_id": dispute.ID, "pull_request_id": dispute.PullRequestID,
			"repository_id": dispute.RepositoryID, "user_id": userID, "semantic_hash": semanticHash,
		}); err != nil {
			return false, fmt.Errorf("record duplicate reassessment admission: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE code_review_decision_disputes
			SET reassessment_status = 'deduped', semantic_input_hash_at_rerun = @semantic_hash,
			    status_detail = 'An equivalent reassessment was already requested for this pull request.',
			    updated_at = now(), version = version + 1
			WHERE org_id = @org_id AND id = @id`, pgx.NamedArgs{
			"org_id": dispute.OrgID, "id": dispute.ID, "semantic_hash": semanticHash,
		}); err != nil {
			return false, fmt.Errorf("mark duplicate reassessment: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit duplicate reassessment: %w", err)
		}
		return false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("check equivalent reassessment admission: %w", err)
	}

	if maxActive > 0 {
		var active int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM code_review_decision_disputes
			WHERE reassessment_status IN ('queued', 'running')`).Scan(&active); err != nil {
			return false, fmt.Errorf("count active reassessments for admission: %w", err)
		}
		if active >= maxActive {
			if _, err := tx.Exec(ctx, `INSERT INTO code_review_reassessment_admissions
				(org_id, dispute_id, pull_request_id, repository_id, user_id, semantic_input_hash, status, denial_reason)
				VALUES (@org_id, @dispute_id, @pull_request_id, @repository_id, @user_id, @semantic_hash, 'denied', 'operator_active_ceiling')`, pgx.NamedArgs{
				"org_id": dispute.OrgID, "dispute_id": dispute.ID, "pull_request_id": dispute.PullRequestID,
				"repository_id": dispute.RepositoryID, "user_id": userID, "semantic_hash": semanticHash,
			}); err != nil {
				return false, fmt.Errorf("record denied reassessment admission: %w", err)
			}
			if _, err := tx.Exec(ctx, `UPDATE code_review_decision_disputes
				SET routing = 'policy_signal_only', reassessment_status = 'not_requested',
				    semantic_input_hash_at_rerun = @semantic_hash,
				    status_detail = 'The objection was recorded, but automatic reassessment is temporarily unavailable.',
				    updated_at = now(), version = version + 1
				WHERE org_id = @org_id AND id = @id`, pgx.NamedArgs{
				"org_id": dispute.OrgID, "id": dispute.ID, "semantic_hash": semanticHash,
			}); err != nil {
				return false, fmt.Errorf("mark reassessment temporarily unavailable: %w", err)
			}
			if err := tx.Commit(ctx); err != nil {
				return false, fmt.Errorf("commit denied reassessment admission: %w", err)
			}
			return false, nil
		}
	}

	var admissionID uuid.UUID
	err = tx.QueryRow(ctx, `INSERT INTO code_review_reassessment_admissions
		(org_id, dispute_id, pull_request_id, repository_id, user_id, semantic_input_hash, status, admitted_at)
		SELECT @org_id, @dispute_id, @pull_request_id, @repository_id, @user_id, @semantic_hash, 'admitted', now()
		FROM code_review_decision_disputes
		WHERE org_id = @org_id AND id = @dispute_id
		  AND pull_request_id = @pull_request_id AND repository_id = @repository_id
		RETURNING id`, pgx.NamedArgs{
		"org_id": dispute.OrgID, "dispute_id": dispute.ID, "pull_request_id": dispute.PullRequestID,
		"repository_id": dispute.RepositoryID, "user_id": userID, "semantic_hash": semanticHash,
	}).Scan(&admissionID)
	if err != nil {
		return false, fmt.Errorf("insert reassessment admission: %w", err)
	}
	dedupeKey := "code_review_dispute_reassessment:" + dispute.ID.String()
	jobID, err := s.jobs.EnqueueInTxWithOpts(ctx, tx, dispute.OrgID, EnqueueOpts{
		Queue: "agent", JobType: models.JobTypeStartCodeReviewReassessment, Payload: payload,
		Priority: 5, DedupeKey: &dedupeKey, MaxAttempts: 8,
	})
	if err != nil {
		return false, fmt.Errorf("enqueue admitted reassessment: %w", err)
	}
	_, err = tx.Exec(ctx, `UPDATE code_review_decision_disputes
		SET reassessment_status = 'queued', semantic_input_hash_at_rerun = @semantic_hash,
		    status_detail = 'Reassessment queued.', updated_at = now(), version = version + 1
		WHERE org_id = @org_id AND id = @id`, pgx.NamedArgs{"org_id": dispute.OrgID, "id": dispute.ID, "semantic_hash": semanticHash})
	if err != nil {
		return false, fmt.Errorf("mark reassessment queued: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit reassessment admission: %w", err)
	}
	s.jobs.Notify(ctx, jobID)
	return true, nil
}

func (s *CodeReviewDisputeStore) MarkReassessmentStarted(ctx context.Context, orgID, disputeID, sessionID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `UPDATE code_review_decision_disputes
		SET reassessment_session_id = @session_id, reassessment_status = 'running',
		    status_detail = 'Reassessment started.', updated_at = now(), version = version + 1
		WHERE org_id = @org_id AND id = @id AND reassessment_status IN ('queued', 'running')`, pgx.NamedArgs{"org_id": orgID, "id": disputeID, "session_id": sessionID})
	if err != nil {
		return fmt.Errorf("mark code review reassessment started: %w", err)
	}
	return nil
}

func (s *CodeReviewDisputeStore) CompleteReassessment(ctx context.Context, orgID, disputeID, sessionID uuid.UUID, status models.CodeReviewSessionStatus, decision *models.CodeReviewDecision, detail string) error {
	_, err := s.CompleteReassessmentOnce(ctx, orgID, disputeID, sessionID, status, decision, detail)
	return err
}

func (s *CodeReviewDisputeStore) CompleteReassessmentOnce(ctx context.Context, orgID, disputeID, sessionID uuid.UUID, status models.CodeReviewSessionStatus, decision *models.CodeReviewDecision, detail string) (bool, error) {
	reassessmentStatus := models.CodeReviewDisputeReassessmentFailed
	if status == models.CodeReviewSessionStatusCompleted && decision != nil {
		reassessmentStatus = models.CodeReviewDisputeReassessmentCompleted
	}
	result, err := s.db.Exec(ctx, `UPDATE code_review_decision_disputes
		SET reassessment_session_id = @session_id, reassessment_decision = @decision,
		    reassessment_flipped = CASE WHEN @decision::text IS NULL THEN NULL ELSE decision IS DISTINCT FROM @decision END,
		    reassessment_status = @reassessment_status, status_detail = NULLIF(@detail, ''),
		    reply_status = CASE WHEN source = 'github_comment' AND reply_status <> 'published' THEN 'pending' ELSE reply_status END,
		    updated_at = now(), version = version + 1
		WHERE org_id = @org_id AND id = @id
		  AND reassessment_status IN ('queued', 'running')`, pgx.NamedArgs{
		"org_id": orgID, "id": disputeID, "session_id": sessionID, "decision": decision,
		"reassessment_status": reassessmentStatus, "detail": strings.TrimSpace(detail),
	})
	if err != nil {
		return false, fmt.Errorf("complete code review reassessment: %w", err)
	}
	return result.RowsAffected() == 1, nil
}

func (s *CodeReviewDisputeStore) MarkHeadChanged(ctx context.Context, orgID, disputeID uuid.UUID, detail string) error {
	_, err := s.db.Exec(ctx, `UPDATE code_review_decision_disputes
		SET reassessment_status = 'head_changed', status_detail = @detail,
		    reply_status = CASE WHEN source = 'github_comment' THEN 'pending' ELSE 'not_applicable' END,
		    updated_at = now(), version = version + 1
		WHERE org_id = @org_id AND id = @id`, pgx.NamedArgs{"org_id": orgID, "id": disputeID, "detail": strings.TrimSpace(detail)})
	if err != nil {
		return fmt.Errorf("mark dispute head changed: %w", err)
	}
	return nil
}

func (s *CodeReviewDisputeStore) MarkReassessmentFailed(ctx context.Context, orgID, disputeID uuid.UUID, detail string) error {
	_, err := s.db.Exec(ctx, `UPDATE code_review_decision_disputes
		SET reassessment_status = 'failed', status_detail = @detail,
		    reply_status = CASE WHEN source = 'github_comment' THEN 'pending' ELSE 'not_applicable' END,
		    updated_at = now(), version = version + 1
		WHERE org_id = @org_id AND id = @id
		  AND reassessment_status IN ('not_requested', 'queued', 'running')`, pgx.NamedArgs{
		"org_id": orgID, "id": disputeID, "detail": strings.TrimSpace(detail),
	})
	if err != nil {
		return fmt.Errorf("mark code review dispute reassessment failed: %w", err)
	}
	return nil
}

func (s *CodeReviewDisputeStore) MarkReplyPublished(ctx context.Context, orgID, disputeID uuid.UUID, commentID *int64) error {
	_, err := s.db.Exec(ctx, `UPDATE code_review_decision_disputes
		SET reply_status = 'published', reply_comment_id = COALESCE(reply_comment_id, @comment_id),
		    updated_at = now(), version = version + 1
		WHERE org_id = @org_id AND id = @id`, pgx.NamedArgs{"org_id": orgID, "id": disputeID, "comment_id": commentID})
	if err != nil {
		return fmt.Errorf("mark dispute reply published: %w", err)
	}
	return nil
}

// ReserveReplyCycle applies the machine-only conversation budget before the
// first GitHub reply for a dispute. Updating that same reply later reuses the
// reservation and does not consume another cycle.
func (s *CodeReviewDisputeStore) ReserveReplyCycle(ctx context.Context, orgID, disputeID uuid.UUID, maxCycles int) (bool, error) {
	if maxCycles <= 0 {
		return false, fmt.Errorf("reply cycle budget must be positive")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin dispute reply cycle reservation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var pullRequestID uuid.UUID
	var reserved bool
	var cycles int
	if err := tx.QueryRow(ctx, `SELECT dispute.pull_request_id, dispute.reply_cycle_reserved,
			pull_request.code_review_dispute_cycles_in_epoch
		FROM code_review_decision_disputes dispute
		JOIN pull_requests pull_request
		  ON pull_request.org_id = dispute.org_id AND pull_request.id = dispute.pull_request_id
		WHERE dispute.org_id = @org_id AND dispute.id = @dispute_id
		FOR UPDATE OF dispute, pull_request`, pgx.NamedArgs{
		"org_id": orgID, "dispute_id": disputeID,
	}).Scan(&pullRequestID, &reserved, &cycles); err != nil {
		return false, fmt.Errorf("load dispute reply cycle state: %w", err)
	}
	if reserved {
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit existing dispute reply reservation: %w", err)
		}
		return true, nil
	}
	if cycles >= maxCycles {
		if _, err := tx.Exec(ctx, `UPDATE code_review_decision_disputes
			SET reply_status = 'failed',
			    status_detail = 'The automatic reply was stopped by the conversation loop guard.',
			    updated_at = now(), version = version + 1
			WHERE org_id = @org_id AND id = @dispute_id`, pgx.NamedArgs{
			"org_id": orgID, "dispute_id": disputeID,
		}); err != nil {
			return false, fmt.Errorf("record dispute reply loop guard: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit dispute reply loop guard: %w", err)
		}
		return false, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE pull_requests
		SET code_review_dispute_cycles_in_epoch = code_review_dispute_cycles_in_epoch + 1
		WHERE org_id = @org_id AND id = @pull_request_id`, pgx.NamedArgs{
		"org_id": orgID, "pull_request_id": pullRequestID,
	}); err != nil {
		return false, fmt.Errorf("spend dispute reply cycle: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE code_review_decision_disputes
		SET reply_cycle_reserved = true, updated_at = now(), version = version + 1
		WHERE org_id = @org_id AND id = @dispute_id`, pgx.NamedArgs{
		"org_id": orgID, "dispute_id": disputeID,
	}); err != nil {
		return false, fmt.Errorf("reserve dispute reply cycle: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit dispute reply cycle reservation: %w", err)
	}
	return true, nil
}

func (s *CodeReviewDisputeStore) MarkReplyFailed(ctx context.Context, orgID, disputeID uuid.UUID, detail string) error {
	_, err := s.db.Exec(ctx, `UPDATE code_review_decision_disputes
		SET reply_status = 'failed', status_detail = COALESCE(NULLIF(@detail, ''), status_detail),
		    updated_at = now(), version = version + 1
		WHERE org_id = @org_id AND id = @id AND reply_status <> 'published'`, pgx.NamedArgs{"org_id": orgID, "id": disputeID, "detail": strings.TrimSpace(detail)})
	if err != nil {
		return fmt.Errorf("mark dispute reply failed: %w", err)
	}
	return nil
}

func (s *CodeReviewDisputeStore) Escalate(ctx context.Context, orgID, disputeID, userID uuid.UUID, note string) (models.CodeReviewDispute, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return models.CodeReviewDispute{}, fmt.Errorf("begin code review dispute escalation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	note = strings.TrimSpace(note)
	if _, err := tx.Exec(ctx, `INSERT INTO code_review_dispute_escalations (org_id, dispute_id, user_id, note)
		SELECT @org_id, id, @user_id, @note
		FROM code_review_decision_disputes
		WHERE org_id = @org_id AND id = @id AND routing = 'policy_signal_only'
		  AND (adjudication_status IS NULL OR adjudication_status = 'pending')
		ON CONFLICT (org_id, dispute_id, user_id) DO NOTHING`, pgx.NamedArgs{
		"org_id": orgID, "id": disputeID, "user_id": userID, "note": note,
	}); err != nil {
		return models.CodeReviewDispute{}, fmt.Errorf("record code review dispute escalation: %w", err)
	}
	rows, err := tx.Query(ctx, `UPDATE code_review_decision_disputes
		SET escalated_at = COALESCE(escalated_at, now()), escalated_by_user_id = COALESCE(escalated_by_user_id, @user_id),
		    adjudication_status = COALESCE(adjudication_status, 'pending'),
		    status_detail = CASE WHEN escalated_at IS NULL THEN COALESCE(NULLIF(@note, ''), status_detail) ELSE status_detail END,
		    updated_at = CASE WHEN escalated_at IS NULL THEN now() ELSE updated_at END,
		    version = CASE WHEN escalated_at IS NULL THEN version + 1 ELSE version END
		WHERE org_id = @org_id AND id = @id AND routing = 'policy_signal_only'
		  AND (adjudication_status IS NULL OR adjudication_status = 'pending')
		RETURNING `+codeReviewDisputeColumns, pgx.NamedArgs{"org_id": orgID, "id": disputeID, "user_id": userID, "note": note})
	if err != nil {
		return models.CodeReviewDispute{}, fmt.Errorf("escalate code review dispute: %w", err)
	}
	result, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CodeReviewDispute])
	if err != nil {
		return models.CodeReviewDispute{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return models.CodeReviewDispute{}, fmt.Errorf("commit code review dispute escalation: %w", err)
	}
	return result, nil
}

func (s *CodeReviewDisputeStore) Adjudicate(ctx context.Context, orgID, disputeID, userID uuid.UUID, update models.CodeReviewDisputeAdjudicationUpdate) (models.CodeReviewDispute, error) {
	if update.ExpectedVersion <= 0 {
		return models.CodeReviewDispute{}, fmt.Errorf("expected_version must be positive")
	}
	rows, err := s.db.Query(ctx, `UPDATE code_review_decision_disputes
		SET adjudication_status = CASE
				WHEN @adjudication_status::text IS NOT NULL THEN @adjudication_status
				WHEN @trust_override_present AND COALESCE(
					@trust_override,
					repository_visibility = 'private' OR upper(btrim(author_association)) IN ('OWNER', 'MEMBER', 'COLLABORATOR')
				) THEN COALESCE(adjudication_status, 'pending')
				WHEN @trust_override_present AND adjudication_status = 'pending' THEN NULL
				ELSE adjudication_status
			END,
		    adjudicated_by_user_id = CASE WHEN @adjudication_status::text IS NULL THEN adjudicated_by_user_id ELSE @user_id END,
		    adjudicated_at = CASE WHEN @adjudication_status::text IS NULL THEN adjudicated_at ELSE now() END,
		    adjudication_note = COALESCE(@adjudication_note, adjudication_note),
		    trust_override = CASE WHEN @trust_override_present THEN @trust_override ELSE trust_override END,
		    updated_at = now(), version = version + 1
		WHERE org_id = @org_id AND id = @id AND version = @expected_version
		  AND (@adjudication_status::text IS NULL OR adjudication_status = 'pending')
		RETURNING `+codeReviewDisputeColumns, pgx.NamedArgs{
		"org_id": orgID, "id": disputeID, "user_id": userID, "expected_version": update.ExpectedVersion,
		"adjudication_status": update.AdjudicationStatus, "adjudication_note": update.AdjudicationNote,
		"trust_override_present": update.TrustOverridePresent, "trust_override": update.TrustOverride,
	})
	if err != nil {
		return models.CodeReviewDispute{}, fmt.Errorf("adjudicate code review dispute: %w", err)
	}
	result, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CodeReviewDispute])
	if errors.Is(err, pgx.ErrNoRows) {
		return models.CodeReviewDispute{}, ErrCodeReviewDisputeVersionConflict
	}
	return result, err
}
