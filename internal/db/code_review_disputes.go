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
	source_updated_at,
	body, contested_reason_codes, dispute_kind, asserts_new_information, routing, intake_status,
	intake_confidence, reassessment_session_id, reassessment_decision, reassessment_flipped,
	reassessment_status, semantic_input_hash_at_filing, semantic_input_hash_at_rerun,
	adjudication_status, adjudicated_by_user_id, adjudicated_at, adjudication_note, policy_owner_active_seconds, escalated_at,
	escalated_by_user_id, queue_signals, queue_priority, reply_status, reply_cycle_reserved,
	superseded_by_dispute_id, status_detail, version,
	created_at, updated_at`

var qualifiedCodeReviewDisputeColumns = func() string {
	columns := strings.Split(codeReviewDisputeColumns, ",")
	for i := range columns {
		columns[i] = "dispute." + strings.TrimSpace(columns[i])
	}
	return strings.Join(columns, ", ")
}()

var (
	ErrCodeReviewDisputeVersionConflict    = errors.New("code review dispute version conflict")
	ErrCodeReviewDisputeQueueCursorExpired = errors.New("code review dispute queue cursor expired")
	// ErrCodeReviewDisputeIntakeCapped reports that a filer reached a rolling
	// intake ceiling for a pull request. It is an admission decision, not a
	// failure: callers record the comment as ordinary feedback.
	ErrCodeReviewDisputeIntakeCapped = errors.New("code review dispute intake cap reached")
)

// CodeReviewDisputeIntakeGuard bounds how much GitHub traffic may open disputes
// on one pull request. A zero value disables the guard.
//
// PerPullRequestMax counts every GitHub-sourced dispute on the pull request,
// trusted or not, and callers apply it to both. That keeps the trust rule in one
// place (models.CodeReviewDispute.CurrentTrust) instead of duplicating it in
// SQL. PerLoginMax is the untrusted-only ceiling, so the two together mean a
// crowd of throwaway accounts and a single busy maintainer are both bounded,
// with the per-login figure biting first for drive-by traffic.
type CodeReviewDisputeIntakeGuard struct {
	Window            time.Duration
	PerLoginMax       int
	PerPullRequestMax int
}

func (g CodeReviewDisputeIntakeGuard) enabled() bool {
	return g.Window > 0 && (g.PerLoginMax > 0 || g.PerPullRequestMax > 0)
}

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

// CreateAndEnqueueTriage stores a dispute and enqueues its triage atomically.
// When guard is enabled the intake ceilings are evaluated inside the same
// transaction, so concurrent webhook deliveries cannot each observe room under
// the cap and both insert.
func (s *CodeReviewDisputeStore) CreateAndEnqueueTriage(ctx context.Context, dispute *models.CodeReviewDispute, guard CodeReviewDisputeIntakeGuard) (bool, error) {
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

	if guard.enabled() {
		// Serialize intake decisions per pull request. Collisions across pull
		// requests only add brief serialization, never a wrong verdict.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(@namespace, @pull_request_key)`, pgx.NamedArgs{
			"namespace": int32(codeReviewDisputeIntakeLockNamespace), "pull_request_key": advisoryLockKeyForUUID(dispute.PullRequestID),
		}); err != nil {
			return false, fmt.Errorf("lock code review dispute intake: %w", err)
		}
		// Recognize a redelivery before judging admission. A stored dispute
		// counts toward its own ceiling, so the comment that consumed the last
		// slot would otherwise be reported as capped -- and therefore as never
		// captured -- on any redelivery of it. Without the guard the insert's
		// ON CONFLICT path below already handles this, so the extra read only
		// runs on the capped path.
		if dispute.GitHubCommentID != nil {
			existing, getErr := getCodeReviewDisputeByGitHubSource(ctx, tx, dispute.OrgID, *dispute.GitHubCommentID, dispute.SourceVersion)
			if getErr == nil {
				*dispute = existing
				return false, nil
			}
			if !errors.Is(getErr, pgx.ErrNoRows) {
				return false, fmt.Errorf("look up existing GitHub dispute source: %w", getErr)
			}
		}
		var byLogin, byPullRequest int
		if err := tx.QueryRow(ctx, `SELECT
				count(*) FILTER (WHERE lower(btrim(filed_by_login)) = lower(btrim(@filed_by_login))),
				count(*)
			FROM code_review_decision_disputes
			WHERE org_id = @org_id
			  AND pull_request_id = @pull_request_id
			  AND source = 'github_comment'
			  AND created_at >= now() - make_interval(secs => @window_seconds)`, pgx.NamedArgs{
			"org_id": dispute.OrgID, "pull_request_id": dispute.PullRequestID,
			"filed_by_login": strings.TrimSpace(dispute.FiledByLogin),
			"window_seconds": int64(guard.Window / time.Second),
		}).Scan(&byLogin, &byPullRequest); err != nil {
			return false, fmt.Errorf("count recent code review dispute intake: %w", err)
		}
		if (guard.PerLoginMax > 0 && byLogin >= guard.PerLoginMax) ||
			(guard.PerPullRequestMax > 0 && byPullRequest >= guard.PerPullRequestMax) {
			return false, ErrCodeReviewDisputeIntakeCapped
		}
	}

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
	// A new epoch means a new human turn in the conversation. Editing one GitHub
	// comment is not a new turn: every edit yields a fresh source_version and so
	// a fresh dispute row, and resetting here would hand each edit a full reply
	// budget again -- the loop guard could then never fire. Only the first
	// dispute filed against a given comment (and every non-GitHub filing) opens
	// an epoch.
	if _, err := tx.Exec(ctx, `UPDATE pull_requests
		SET code_review_dispute_epoch = code_review_dispute_epoch + 1,
		    code_review_dispute_cycles_in_epoch = 0
		WHERE org_id = @org_id AND id = @pull_request_id
		  AND (@github_comment_id::bigint IS NULL OR NOT EXISTS (
			SELECT 1 FROM code_review_decision_disputes prior
			WHERE prior.org_id = @org_id AND prior.github_comment_id = @github_comment_id
			  AND prior.id <> @dispute_id
		  ))`, pgx.NamedArgs{
		"org_id": created.OrgID, "pull_request_id": created.PullRequestID,
		"github_comment_id": created.GitHubCommentID, "dispute_id": created.ID,
	}); err != nil {
		return false, fmt.Errorf("reset code review dispute bot-loop epoch: %w", err)
	}
	if err := reconcileCodeReviewDisputeSupersession(ctx, tx, created); err != nil {
		return false, err
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

// reconcileCodeReviewDisputeSupersession re-points every dispute filed against
// one GitHub comment at whichever of them is currently newest, and clears the
// pointer on that newest one. Every edit files a new row (source_version is
// content-derived) and each inherits the thread's single reply comment, so
// without exactly one live row two disputes share one GitHub comment and
// whichever reply job runs last wins -- adjudicating a replaced objection would
// rewrite the answer to the current one.
//
// The retirement is recorded in superseded_by_dispute_id, not in reply_status.
// reply_status is a lifecycle column that CompleteReassessment, FailTriage,
// and MarkReassessmentFailed all reset to 'pending' -- and
// BuildReply calls CompleteReassessment itself, so a retirement stored there
// would be undone by the very read that precedes the publish check.
//
// Newest is decided by source_updated_at, the provider's own edit time, not by
// insert order: a failed 'created' delivery can be redelivered after the
// 'edited' that replaced it, and ordering on arrival would then let the stale
// body win. Recomputing the whole set rather than retiring "everything except
// me" also makes this idempotent and independent of arrival order -- a late
// insert cannot leave two live rows, and two of them cannot retire each other.
//
// adjudication_status moves pending -> expired so the queue stops showing one
// row per keystroke-fix, while the row itself stays auditable.
//
// This does not stop a reply job that already passed the publish check before
// this transaction committed. That window is a single job's runtime and cannot
// be closed without holding a lock across a GitHub round trip.
func reconcileCodeReviewDisputeSupersession(ctx context.Context, db DBTX, created models.CodeReviewDispute) error {
	if created.GitHubCommentID == nil {
		return nil
	}
	if _, err := db.Exec(ctx, `WITH newest AS (
			SELECT id FROM code_review_decision_disputes
			WHERE org_id = @org_id AND github_comment_id = @github_comment_id
			ORDER BY COALESCE(source_updated_at, created_at) DESC, created_at DESC, id DESC
			LIMIT 1
		)
		UPDATE code_review_decision_disputes dispute
		SET superseded_by_dispute_id = NULLIF(newest.id, dispute.id),
		    adjudication_status = CASE
				WHEN newest.id <> dispute.id AND dispute.adjudication_status = 'pending' THEN 'expired'
				ELSE dispute.adjudication_status
			END,
		    -- COALESCE, not an overwrite: a completed reassessment's detail is
		    -- the record of what this objection achieved.
		    status_detail = CASE
				WHEN newest.id <> dispute.id
					THEN COALESCE(dispute.status_detail, 'A newer edit of this comment replaced this objection.')
				ELSE dispute.status_detail
			END,
		    updated_at = now(), version = version + 1
		FROM newest
		WHERE dispute.org_id = @org_id AND dispute.github_comment_id = @github_comment_id
		  -- Skip rows already pointing where they should, so a redelivery does
		  -- not bump version and invalidate an admin's open adjudication form.
		  AND dispute.superseded_by_dispute_id IS DISTINCT FROM NULLIF(newest.id, dispute.id)`, pgx.NamedArgs{
		"org_id": created.OrgID, "github_comment_id": created.GitHubCommentID,
	}); err != nil {
		return fmt.Errorf("reconcile code review dispute supersession: %w", err)
	}
	return nil
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
			github_thread_root_comment_id, source_body_hash, source_version, source_updated_at, body,
			contested_reason_codes, semantic_input_hash_at_filing, queue_signals, reply_status,
			reply_comment_id, reply_cycle_reserved
		) SELECT
			@org_id, @session_id, @pull_request_id, @repository_id, @policy_id, @reviewed_head_sha,
			@decision, @direction, @filed_by_user_id, @filed_by_login, @author_association, @author_is_pr_author,
			@repository_visibility, @membership_evidence, @source, @github_comment_id,
			@github_thread_root_comment_id, @source_body_hash, @source_version, @source_updated_at, @body,
			@contested_reason_codes, @semantic_input_hash_at_filing, @queue_signals, @reply_status,
			-- Editing one GitHub comment files a new dispute (the source_version
			-- is content-derived), so inherit the reply this thread already has.
			-- Without this each edit posts an additional bot comment instead of
			-- updating the answer, and re-spends a conversation cycle for a turn
			-- the human never took.
			(SELECT prior.reply_comment_id FROM code_review_decision_disputes prior
			 WHERE prior.org_id = @org_id AND prior.github_comment_id = @github_comment_id
			   AND prior.reply_comment_id IS NOT NULL
			 ORDER BY prior.created_at DESC, prior.id DESC LIMIT 1),
			COALESCE((SELECT true FROM code_review_decision_disputes prior
			 WHERE prior.org_id = @org_id AND prior.github_comment_id = @github_comment_id
			   AND prior.reply_cycle_reserved LIMIT 1), false)
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
		"source_updated_at": dispute.SourceUpdatedAt,
		"body":              strings.TrimSpace(dispute.Body), "contested_reason_codes": dispute.ContestedReasonCodes,
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

	var snapshotID uuid.UUID
	position := int64(0)
	queryDB := DBTX(s.db)
	var tx pgx.Tx
	if filters.Cursor == nil {
		var err error
		tx, err = s.db.Begin(ctx)
		if err != nil {
			return models.CodeReviewDisputePage{}, fmt.Errorf("begin code review dispute queue snapshot: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		queryDB = tx
		snapshotID = uuid.New()

		if _, err := tx.Exec(ctx, `DELETE FROM code_review_dispute_queue_snapshots
			WHERE org_id = @org_id AND expires_at <= now()`, pgx.NamedArgs{"org_id": orgID}); err != nil {
			return models.CodeReviewDisputePage{}, fmt.Errorf("clean expired code review dispute queue snapshots: %w", err)
		}

		args := pgx.NamedArgs{
			"org_id": orgID, "snapshot_id": snapshotID,
			"expires_at": time.Now().UTC().Add(time.Hour),
		}
		where := ` WHERE dispute.org_id = @org_id AND dispute.adjudication_status IS NOT NULL`
		if filters.AdjudicationStatus != nil {
			where += ` AND dispute.adjudication_status = @adjudication_status`
			args["adjudication_status"] = *filters.AdjudicationStatus
		}
		if filters.RepositoryID != nil {
			where += ` AND dispute.repository_id = @repository_id`
			args["repository_id"] = *filters.RepositoryID
		}
		if filters.Direction != nil {
			where += ` AND dispute.direction = @direction`
			args["direction"] = *filters.Direction
		}
		// Materializing the full ordered identity list freezes both priorities and
		// membership for this paging session. A later rerank can update any row
		// without moving it across the client's cursor boundary.
		if _, err := tx.Exec(ctx, `INSERT INTO code_review_dispute_queue_snapshots
				(org_id, snapshot_id, position, dispute_id, expires_at)
			SELECT @org_id, @snapshot_id,
				row_number() OVER (ORDER BY dispute.queue_priority DESC, dispute.created_at DESC, dispute.id DESC),
				dispute.id, @expires_at
			FROM code_review_decision_disputes AS dispute`+where, args); err != nil {
			return models.CodeReviewDisputePage{}, fmt.Errorf("materialize code review dispute queue snapshot: %w", err)
		}
	} else {
		snapshotID = filters.Cursor.SnapshotID
		position = filters.Cursor.Position
		var active bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM code_review_dispute_queue_snapshots
			WHERE org_id = @org_id AND snapshot_id = @snapshot_id AND expires_at > now()
		)`, pgx.NamedArgs{"org_id": orgID, "snapshot_id": snapshotID}).Scan(&active); err != nil {
			return models.CodeReviewDisputePage{}, fmt.Errorf("validate code review dispute queue snapshot: %w", err)
		}
		if !active {
			return models.CodeReviewDisputePage{}, ErrCodeReviewDisputeQueueCursorExpired
		}
	}

	type queueRow struct {
		models.CodeReviewDispute
		SnapshotPosition int64 `db:"snapshot_position"`
	}
	rows, err := queryDB.Query(ctx, `SELECT `+qualifiedCodeReviewDisputeColumns+`, snapshot.position AS snapshot_position
		FROM code_review_dispute_queue_snapshots AS snapshot
		JOIN code_review_decision_disputes AS dispute
		  ON dispute.org_id = snapshot.org_id AND dispute.id = snapshot.dispute_id
		WHERE snapshot.org_id = @org_id
		  AND snapshot.snapshot_id = @snapshot_id
		  AND snapshot.position > @position
		  AND snapshot.expires_at > now()
		ORDER BY snapshot.position
		LIMIT @limit`, pgx.NamedArgs{
		"org_id": orgID, "snapshot_id": snapshotID, "position": position, "limit": limit + 1,
	})
	if err != nil {
		return models.CodeReviewDisputePage{}, fmt.Errorf("list code review dispute queue: %w", err)
	}
	queueRows, err := pgx.CollectRows(rows, pgx.RowToStructByName[queueRow])
	if err != nil {
		return models.CodeReviewDisputePage{}, err
	}
	if tx != nil {
		if err := tx.Commit(ctx); err != nil {
			return models.CodeReviewDisputePage{}, fmt.Errorf("commit code review dispute queue snapshot: %w", err)
		}
	}

	page := models.CodeReviewDisputePage{Items: make([]models.CodeReviewDispute, 0, min(len(queueRows), limit))}
	for i, row := range queueRows {
		if i == limit {
			last := queueRows[limit-1]
			page.NextQueueCursor = &models.CodeReviewDisputeQueueCursor{
				SnapshotID: snapshotID,
				Position:   last.SnapshotPosition,
			}
			break
		}
		page.Items = append(page.Items, row.CodeReviewDispute)
	}
	return page, nil
}

// DeleteExpiredQueueSnapshots reclaims materialized pagination rows even when
// an organization does not open another queue paging session.
func (s *CodeReviewDisputeStore) DeleteExpiredQueueSnapshots(ctx context.Context, orgID uuid.UUID) (int64, error) {
	result, err := s.db.Exec(ctx, `DELETE FROM code_review_dispute_queue_snapshots
		WHERE org_id = @org_id AND expires_at <= now()`, pgx.NamedArgs{"org_id": orgID})
	if err != nil {
		return 0, fmt.Errorf("delete expired code review dispute queue snapshots: %w", err)
	}
	return result.RowsAffected(), nil
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

// codeReviewDisputeIntakeLockNamespace scopes the per-pull-request advisory
// lock that serializes intake admission.
const codeReviewDisputeIntakeLockNamespace = 143122

// advisoryLockKeyForUUID derives a stable int32 advisory-lock key. Postgres's
// two-argument advisory locks take int32s, and deriving the key here avoids
// depending on the undocumented hashtext() builtin. The sign bit is masked off
// so the conversion is representable in int32 rather than an overflowing cast;
// losing one bit only widens key collisions, which cost brief extra
// serialization and never a wrong verdict.
func advisoryLockKeyForUUID(value uuid.UUID) int32 {
	return int32(value[0]&0x7f)<<24 |
		int32(value[1])<<16 |
		int32(value[2])<<8 |
		int32(value[3])
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
		SET intake_status = 'failed', reply_status = CASE WHEN source = 'github_comment' OR direction = 'should_not_have_approved' THEN 'pending' ELSE 'not_applicable' END,
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
		    reply_status = CASE WHEN (source = 'github_comment' OR direction = 'should_not_have_approved') AND reply_status <> 'published' THEN 'pending' ELSE reply_status END,
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

func (s *CodeReviewDisputeStore) MarkReassessmentFailed(ctx context.Context, orgID, disputeID uuid.UUID, detail string) error {
	_, err := s.db.Exec(ctx, `UPDATE code_review_decision_disputes
		SET reassessment_status = 'failed', status_detail = @detail,
		    reply_status = CASE WHEN source = 'github_comment' OR direction = 'should_not_have_approved' THEN 'pending' ELSE 'not_applicable' END,
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
// reservation and does not consume another cycle. The second return value
// reports whether this call took the reservation, which is the durable marker
// that no GitHub reply has ever been attempted for this dispute.
func (s *CodeReviewDisputeStore) ReserveReplyCycle(ctx context.Context, orgID, disputeID uuid.UUID, maxCycles int) (bool, bool, error) {
	if maxCycles <= 0 {
		return false, false, fmt.Errorf("reply cycle budget must be positive")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, false, fmt.Errorf("begin dispute reply cycle reservation: %w", err)
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
		return false, false, fmt.Errorf("load dispute reply cycle state: %w", err)
	}
	if reserved {
		if err := tx.Commit(ctx); err != nil {
			return false, false, fmt.Errorf("commit existing dispute reply reservation: %w", err)
		}
		return true, false, nil
	}
	if cycles >= maxCycles {
		if _, err := tx.Exec(ctx, `UPDATE code_review_decision_disputes
			SET reply_status = 'failed',
			    status_detail = 'The automatic reply was stopped by the conversation loop guard.',
			    updated_at = now(), version = version + 1
			WHERE org_id = @org_id AND id = @dispute_id`, pgx.NamedArgs{
			"org_id": orgID, "dispute_id": disputeID,
		}); err != nil {
			return false, false, fmt.Errorf("record dispute reply loop guard: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return false, false, fmt.Errorf("commit dispute reply loop guard: %w", err)
		}
		return false, false, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE pull_requests
		SET code_review_dispute_cycles_in_epoch = code_review_dispute_cycles_in_epoch + 1
		WHERE org_id = @org_id AND id = @pull_request_id`, pgx.NamedArgs{
		"org_id": orgID, "pull_request_id": pullRequestID,
	}); err != nil {
		return false, false, fmt.Errorf("spend dispute reply cycle: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE code_review_decision_disputes
		SET reply_cycle_reserved = true, updated_at = now(), version = version + 1
		WHERE org_id = @org_id AND id = @dispute_id`, pgx.NamedArgs{
		"org_id": orgID, "dispute_id": disputeID,
	}); err != nil {
		return false, false, fmt.Errorf("reserve dispute reply cycle: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, false, fmt.Errorf("commit dispute reply cycle reservation: %w", err)
	}
	return true, true, nil
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
		  AND superseded_by_dispute_id IS NULL
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
		  AND superseded_by_dispute_id IS NULL
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
	if update.PolicyOwnerActiveSeconds != nil && (*update.PolicyOwnerActiveSeconds < 0 || *update.PolicyOwnerActiveSeconds > 3600) {
		return models.CodeReviewDispute{}, fmt.Errorf("policy_owner_active_seconds must be between 0 and 3600")
	}
	// Promotion may only open an adjudication slot for a dispute the table's
	// own CHECK allows one on. Without the routing guard, promoting an
	// answer_only, not_a_dispute, or intake-failed dispute writes 'pending'
	// and trips the constraint instead of just recording the override.
	rows, err := s.db.Query(ctx, `UPDATE code_review_decision_disputes
		SET adjudication_status = CASE
				WHEN @adjudication_status::text IS NOT NULL THEN @adjudication_status
				WHEN @trust_override_present AND COALESCE(
					@trust_override,
					repository_visibility = 'private' OR upper(btrim(author_association)) IN ('OWNER', 'MEMBER', 'COLLABORATOR')
				) THEN CASE
					WHEN intake_status = 'triaged' AND routing IN ('reassess', 'policy_signal_only') AND direction IS NOT NULL
						THEN COALESCE(adjudication_status, 'pending')
					ELSE adjudication_status
				END
				WHEN @trust_override_present AND adjudication_status = 'pending' THEN NULL
				ELSE adjudication_status
			END,
		    adjudicated_by_user_id = CASE WHEN @adjudication_status::text IS NULL THEN adjudicated_by_user_id ELSE @user_id END,
		    adjudicated_at = CASE WHEN @adjudication_status::text IS NULL THEN adjudicated_at ELSE now() END,
		    adjudication_note = COALESCE(@adjudication_note, adjudication_note),
		    policy_owner_active_seconds = CASE WHEN @adjudication_status::text IS NULL THEN policy_owner_active_seconds ELSE @policy_owner_active_seconds END,
		    trust_override = CASE WHEN @trust_override_present THEN @trust_override ELSE trust_override END,
		    updated_at = now(), version = version + 1
		WHERE org_id = @org_id AND id = @id AND version = @expected_version
		  AND (@adjudication_status::text IS NULL OR adjudication_status = 'pending')
		RETURNING `+codeReviewDisputeColumns, pgx.NamedArgs{
		"org_id": orgID, "id": disputeID, "user_id": userID, "expected_version": update.ExpectedVersion,
		"adjudication_status": update.AdjudicationStatus, "adjudication_note": update.AdjudicationNote,
		"policy_owner_active_seconds": update.PolicyOwnerActiveSeconds,
		"trust_override_present":      update.TrustOverridePresent, "trust_override": update.TrustOverride,
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
