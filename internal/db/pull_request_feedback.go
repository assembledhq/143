package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type PullRequestFeedbackStore struct{ db TxStarter }

func NewPullRequestFeedbackStore(db TxStarter) *PullRequestFeedbackStore {
	return &PullRequestFeedbackStore{db: db}
}

const prFeedbackItemColumns = `id, org_id, pull_request_id, batch_id, surface, provider_object_id,
 github_delivery_id, github_review_id, github_thread_root_comment_id, in_reply_to_comment_id,
 github_app_id, github_app_slug, author_login, author_type, author_association, bot_eligibility_source,
 body, body_hash, processed_body_hash, provider_finding_key, finding_fingerprint, automatic_attempt_count,
 path, line, side, diff_hunk, comment_commit_sha, observed_head_sha, intent, status, ignore_reason,
 github_response_comment_id, response_body, response_commit_sha, provider_created_at, provider_updated_at,
 received_at, processed_at, updated_at`

// UpsertItem absorbs webhook redelivery and body edits. A changed body is made
// pending again, while an identical redelivery leaves processing state intact.
func (s *PullRequestFeedbackStore) UpsertItem(ctx context.Context, orgID uuid.UUID, item *models.PullRequestFeedbackItem) error {
	query := `INSERT INTO pull_request_feedback_items
 (org_id,pull_request_id,surface,provider_object_id,github_delivery_id,github_review_id,
 github_thread_root_comment_id,in_reply_to_comment_id,github_app_id,github_app_slug,author_login,
 author_type,author_association,bot_eligibility_source,body,body_hash,provider_finding_key,
 finding_fingerprint,path,line,side,diff_hunk,comment_commit_sha,observed_head_sha,intent,status,
 ignore_reason,provider_created_at,provider_updated_at)
 VALUES (@org_id,@pull_request_id,@surface,@provider_object_id,@github_delivery_id,@github_review_id,
 @github_thread_root_comment_id,@in_reply_to_comment_id,@github_app_id,@github_app_slug,@author_login,
 @author_type,@author_association,@bot_eligibility_source,@body,@body_hash,@provider_finding_key,
 @finding_fingerprint,@path,@line,@side,@diff_hunk,@comment_commit_sha,@observed_head_sha,@intent,@status,
 @ignore_reason,@provider_created_at,@provider_updated_at)
 ON CONFLICT (pull_request_id,surface,provider_object_id) DO UPDATE SET
 github_delivery_id=EXCLUDED.github_delivery_id, body=EXCLUDED.body, body_hash=EXCLUDED.body_hash,
 provider_updated_at=EXCLUDED.provider_updated_at, updated_at=now(),
 status=CASE WHEN pull_request_feedback_items.body_hash IS DISTINCT FROM EXCLUDED.body_hash THEN 'pending' ELSE pull_request_feedback_items.status END,
 batch_id=CASE WHEN pull_request_feedback_items.body_hash IS DISTINCT FROM EXCLUDED.body_hash THEN NULL ELSE pull_request_feedback_items.batch_id END,
 automatic_attempt_count=CASE WHEN pull_request_feedback_items.body_hash IS DISTINCT FROM EXCLUDED.body_hash THEN 0 ELSE pull_request_feedback_items.automatic_attempt_count END
 WHERE pull_request_feedback_items.org_id=EXCLUDED.org_id
 RETURNING ` + prFeedbackItemColumns
	args := pgx.NamedArgs{"org_id": orgID, "pull_request_id": item.PullRequestID, "surface": item.Surface, "provider_object_id": item.ProviderObjectID, "github_delivery_id": item.GitHubDeliveryID, "github_review_id": item.GitHubReviewID, "github_thread_root_comment_id": item.GitHubThreadRootCommentID, "in_reply_to_comment_id": item.InReplyToCommentID, "github_app_id": item.GitHubAppID, "github_app_slug": item.GitHubAppSlug, "author_login": item.AuthorLogin, "author_type": item.AuthorType, "author_association": item.AuthorAssociation, "bot_eligibility_source": item.BotEligibilitySource, "body": item.Body, "body_hash": item.BodyHash, "provider_finding_key": item.ProviderFindingKey, "finding_fingerprint": item.FindingFingerprint, "path": item.Path, "line": item.Line, "side": item.Side, "diff_hunk": item.DiffHunk, "comment_commit_sha": item.CommentCommitSHA, "observed_head_sha": item.ObservedHeadSHA, "intent": item.Intent, "status": item.Status, "ignore_reason": item.IgnoreReason, "provider_created_at": item.ProviderCreatedAt, "provider_updated_at": item.ProviderUpdatedAt}
	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return fmt.Errorf("upsert PR feedback item: %w", err)
	}
	got, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PullRequestFeedbackItem])
	if err != nil {
		return fmt.Errorf("collect PR feedback item: %w", err)
	}
	*item = got
	return nil
}

// ClaimBatch atomically locks the PR row, creates a new queued batch, and
// assigns every currently-unclaimed pending item to it as an immutable
// snapshot. The partial unique index (idx_pr_feedback_one_active) permits only
// one non-terminal batch per PR, so a concurrent claim while a batch is already
// active fails on batch insert. Returns pgx.ErrNoRows when there is nothing
// pending to claim.
func (s *PullRequestFeedbackStore) ClaimBatch(ctx context.Context, orgID, pullRequestID uuid.UUID, now time.Time) (*models.PullRequestFeedbackBatch, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin PR feedback claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var sessionID uuid.UUID
	var headSHA string
	err = tx.QueryRow(ctx, `SELECT session_id, COALESCE(head_sha,'') FROM pull_requests WHERE id=$1 AND org_id=$2 FOR UPDATE`, pullRequestID, orgID).Scan(&sessionID, &headSHA)
	if err != nil {
		return nil, fmt.Errorf("lock pull request for feedback: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT `+prFeedbackItemColumns+` FROM pull_request_feedback_items WHERE org_id=$1 AND pull_request_id=$2 AND status='pending' AND batch_id IS NULL ORDER BY received_at,id FOR UPDATE SKIP LOCKED`, orgID, pullRequestID)
	if err != nil {
		return nil, fmt.Errorf("select pending PR feedback: %w", err)
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.PullRequestFeedbackItem])
	if err != nil {
		return nil, fmt.Errorf("collect pending PR feedback: %w", err)
	}
	if len(items) == 0 {
		return nil, pgx.ErrNoRows
	}
	snapshot, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("snapshot PR feedback: %w", err)
	}
	source := models.PRFeedbackBatchSourceBotOnly
	for _, item := range items {
		if item.AuthorType != "Bot" {
			source = models.PRFeedbackBatchSourceHumanOrMixed
			break
		}
	}
	var batch models.PullRequestFeedbackBatch
	err = tx.QueryRow(ctx, `INSERT INTO pull_request_feedback_batches (org_id,pull_request_id,session_id,status,source_kind,expected_head_sha,feedback_snapshot,debounce_until,max_collect_until) VALUES ($1,$2,$3,'queued',$4,$5,$6,$7,$8) RETURNING id,created_at,updated_at`, orgID, pullRequestID, sessionID, source, headSHA, snapshot, now, now).Scan(&batch.ID, &batch.CreatedAt, &batch.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert PR feedback batch: %w", err)
	}
	ids := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	if _, err = tx.Exec(ctx, `UPDATE pull_request_feedback_items SET batch_id=$1,status='claimed',updated_at=now() WHERE org_id=$2 AND pull_request_id=$3 AND id=ANY($4) AND status='pending' AND batch_id IS NULL`, batch.ID, orgID, pullRequestID, ids); err != nil {
		return nil, fmt.Errorf("claim PR feedback items: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit PR feedback claim: %w", err)
	}
	batch.OrgID = orgID
	batch.PullRequestID = pullRequestID
	batch.SessionID = sessionID
	batch.Status = models.PRFeedbackBatchStatusQueued
	batch.SourceKind = source
	batch.ExpectedHeadSHA = headSHA
	batch.FeedbackSnapshot = snapshot
	batch.DebounceUntil = now
	batch.MaxCollectUntil = now
	return &batch, nil
}

func (s *PullRequestFeedbackStore) AdvanceBatch(ctx context.Context, orgID, batchID uuid.UUID, from, to models.PRFeedbackBatchStatus) (bool, error) {
	res, err := s.db.Exec(ctx, `UPDATE pull_request_feedback_batches SET status=$1,started_at=CASE WHEN $1='running' THEN COALESCE(started_at,now()) ELSE started_at END,completed_at=CASE WHEN $1 IN ('completed','needs_attention','cancelled') THEN COALESCE(completed_at,now()) ELSE completed_at END,updated_at=now() WHERE id=$2 AND org_id=$3 AND status=$4`, to, batchID, orgID, from)
	if err != nil {
		return false, fmt.Errorf("advance PR feedback batch: %w", err)
	}
	return res.RowsAffected() == 1, nil
}
