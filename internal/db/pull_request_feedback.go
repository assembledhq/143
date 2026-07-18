package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type PullRequestFeedbackStore struct {
	db   TxStarter
	jobs *JobStore
}

func NewPullRequestFeedbackStore(db TxStarter) *PullRequestFeedbackStore {
	return &PullRequestFeedbackStore{db: db}
}

// SetJobStore wires the async job store used to enqueue feedback collection.
// lint:allow-no-orgid reason="dependency injection only; this method performs no database query"
func (s *PullRequestFeedbackStore) SetJobStore(jobs *JobStore) { s.jobs = jobs }

const prFeedbackItemColumns = `id, org_id, pull_request_id, batch_id, surface, provider_object_id,
 github_delivery_id, github_review_id, github_thread_root_comment_id, in_reply_to_comment_id,
 github_app_id, github_app_slug, author_login, author_type, author_association, bot_eligibility_source,
 body, body_hash, processed_body_hash, provider_finding_key, finding_fingerprint, automatic_attempt_count,
 path, line, side, diff_hunk, comment_commit_sha, observed_head_sha, intent, status, ignore_reason,
 github_response_comment_id, response_body, response_commit_sha, provider_created_at, provider_updated_at,
 received_at, processed_at, updated_at`

const prFeedbackBatchColumns = `id, org_id, pull_request_id, session_id, thread_id, status, source_kind,
 bot_feedback_epoch, expected_head_sha, result_head_sha, workspace_mode, feedback_snapshot,
 debounce_until, max_collect_until, attempt_count, result_summary, error_code, error_detail,
 started_at, completed_at, created_at, updated_at`

// UpsertItem absorbs webhook redelivery and body edits. A changed body is made
// pending again, while an identical redelivery leaves processing state intact.
func (s *PullRequestFeedbackStore) UpsertItem(ctx context.Context, orgID uuid.UUID, item *models.PullRequestFeedbackItem) error {
	return upsertPRFeedbackItem(ctx, s.db, orgID, item)
}

func upsertPRFeedbackItem(ctx context.Context, q DBTX, orgID uuid.UUID, item *models.PullRequestFeedbackItem) error {
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
	 github_delivery_id=EXCLUDED.github_delivery_id,github_review_id=EXCLUDED.github_review_id,
	 github_thread_root_comment_id=EXCLUDED.github_thread_root_comment_id,in_reply_to_comment_id=EXCLUDED.in_reply_to_comment_id,
	 github_app_id=EXCLUDED.github_app_id,github_app_slug=EXCLUDED.github_app_slug,author_login=EXCLUDED.author_login,
	 author_type=EXCLUDED.author_type,author_association=EXCLUDED.author_association,bot_eligibility_source=EXCLUDED.bot_eligibility_source,
	 body=EXCLUDED.body,body_hash=EXCLUDED.body_hash,provider_finding_key=EXCLUDED.provider_finding_key,
	 finding_fingerprint=EXCLUDED.finding_fingerprint,path=EXCLUDED.path,line=EXCLUDED.line,side=EXCLUDED.side,
	 diff_hunk=EXCLUDED.diff_hunk,comment_commit_sha=EXCLUDED.comment_commit_sha,observed_head_sha=EXCLUDED.observed_head_sha,
	 intent=EXCLUDED.intent,ignore_reason=EXCLUDED.ignore_reason,provider_created_at=COALESCE(pull_request_feedback_items.provider_created_at,EXCLUDED.provider_created_at),
	 provider_updated_at=EXCLUDED.provider_updated_at, updated_at=now(),
 status=CASE WHEN pull_request_feedback_items.body_hash IS DISTINCT FROM EXCLUDED.body_hash THEN 'pending' ELSE pull_request_feedback_items.status END,
 batch_id=CASE WHEN pull_request_feedback_items.body_hash IS DISTINCT FROM EXCLUDED.body_hash THEN NULL ELSE pull_request_feedback_items.batch_id END,
 automatic_attempt_count=CASE WHEN pull_request_feedback_items.body_hash IS DISTINCT FROM EXCLUDED.body_hash THEN 0 ELSE pull_request_feedback_items.automatic_attempt_count END
 WHERE pull_request_feedback_items.org_id=EXCLUDED.org_id
 RETURNING ` + prFeedbackItemColumns
	args := pgx.NamedArgs{"org_id": orgID, "pull_request_id": item.PullRequestID, "surface": item.Surface, "provider_object_id": item.ProviderObjectID, "github_delivery_id": item.GitHubDeliveryID, "github_review_id": item.GitHubReviewID, "github_thread_root_comment_id": item.GitHubThreadRootCommentID, "in_reply_to_comment_id": item.InReplyToCommentID, "github_app_id": item.GitHubAppID, "github_app_slug": item.GitHubAppSlug, "author_login": item.AuthorLogin, "author_type": item.AuthorType, "author_association": item.AuthorAssociation, "bot_eligibility_source": item.BotEligibilitySource, "body": item.Body, "body_hash": item.BodyHash, "provider_finding_key": item.ProviderFindingKey, "finding_fingerprint": item.FindingFingerprint, "path": item.Path, "line": item.Line, "side": item.Side, "diff_hunk": item.DiffHunk, "comment_commit_sha": item.CommentCommitSHA, "observed_head_sha": item.ObservedHeadSHA, "intent": item.Intent, "status": item.Status, "ignore_reason": item.IgnoreReason, "provider_created_at": item.ProviderCreatedAt, "provider_updated_at": item.ProviderUpdatedAt}
	rows, err := q.Query(ctx, query, args)
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

// Ingest atomically records the signed provider delivery, normalizes its item,
// and schedules collection. A redelivered GitHub delivery is a successful
// no-op; provider-object uniqueness separately absorbs reconciliation overlap.
func (s *PullRequestFeedbackStore) Ingest(ctx context.Context, delivery *models.WebhookDelivery, item *models.PullRequestFeedbackItem) (bool, error) {
	if s.jobs == nil {
		return false, fmt.Errorf("PR feedback job store is not configured")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin PR feedback ingest: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var deliveryID uuid.UUID
	err = tx.QueryRow(ctx, `INSERT INTO webhook_deliveries (org_id,integration_id,provider,delivery_id,event_type,signature_valid,payload,headers,status) VALUES ($1,$2,'github',$3,$4,true,$5,$6,'processed') ON CONFLICT (provider,delivery_id) WHERE delivery_id IS NOT NULL DO NOTHING RETURNING id`, delivery.OrgID, delivery.IntegrationID, delivery.DeliveryID, delivery.EventType, delivery.Payload, delivery.Headers).Scan(&deliveryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("record GitHub feedback delivery: %w", err)
	}
	delivery.ID = deliveryID
	if err := upsertPRFeedbackItem(ctx, tx, delivery.OrgID, item); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE pull_request_feedback_batches SET debounce_until=LEAST(max_collect_until,now()+interval '5 seconds'),updated_at=now() WHERE org_id=$1 AND pull_request_id=$2 AND status='collecting'`, delivery.OrgID, item.PullRequestID); err != nil {
		return false, fmt.Errorf("extend PR feedback collection window: %w", err)
	}
	dedupeKey := "collect_pr_feedback:" + item.PullRequestID.String()
	jobID, err := s.jobs.EnqueueInTx(ctx, tx, delivery.OrgID, "default", models.JobTypeCollectPullRequestFeedback, map[string]string{"org_id": delivery.OrgID.String(), "pull_request_id": item.PullRequestID.String()}, 5, &dedupeKey)
	if err != nil {
		return false, fmt.Errorf("enqueue PR feedback collection: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit PR feedback ingest: %w", err)
	}
	s.jobs.Notify(ctx, jobID)
	return true, nil
}

func (s *PullRequestFeedbackStore) EnsureCollectingBatch(ctx context.Context, orgID, pullRequestID uuid.UUID, now time.Time) (*models.PullRequestFeedbackBatch, bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin PR feedback collection: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var sessionID *uuid.UUID
	var headSHA string
	err = tx.QueryRow(ctx, `SELECT session_id,COALESCE(head_sha,'') FROM pull_requests WHERE org_id=$1 AND id=$2 AND status='open' FOR UPDATE`, orgID, pullRequestID).Scan(&sessionID, &headSHA)
	if err != nil {
		return nil, false, fmt.Errorf("lock pull request for feedback collection: %w", err)
	}
	if sessionID == nil {
		return nil, false, pgx.ErrNoRows
	}
	rows, err := tx.Query(ctx, `SELECT `+prFeedbackBatchColumns+` FROM pull_request_feedback_batches WHERE org_id=$1 AND pull_request_id=$2 AND status IN ('collecting','queued','running','pushing','responding') ORDER BY created_at DESC LIMIT 1 FOR UPDATE`, orgID, pullRequestID)
	if err != nil {
		return nil, false, fmt.Errorf("find active PR feedback batch: %w", err)
	}
	batch, collectErr := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PullRequestFeedbackBatch])
	if collectErr == nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, false, fmt.Errorf("commit existing PR feedback collection: %w", err)
		}
		return &batch, false, nil
	}
	if !errors.Is(collectErr, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("collect active PR feedback batch: %w", collectErr)
	}
	debounceUntil := now.Add(5 * time.Second)
	maxUntil := now.Add(15 * time.Second)
	rows, err = tx.Query(ctx, `INSERT INTO pull_request_feedback_batches (org_id,pull_request_id,session_id,status,source_kind,expected_head_sha,feedback_snapshot,debounce_until,max_collect_until) VALUES ($1,$2,$3,'collecting','human_or_mixed',$4,'[]',$5,$6) RETURNING `+prFeedbackBatchColumns, orgID, pullRequestID, *sessionID, headSHA, debounceUntil, maxUntil)
	if err != nil {
		return nil, false, fmt.Errorf("create collecting PR feedback batch: %w", err)
	}
	batch, err = pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PullRequestFeedbackBatch])
	if err != nil {
		return nil, false, fmt.Errorf("collect new PR feedback batch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit PR feedback collection: %w", err)
	}
	return &batch, true, nil
}

func (s *PullRequestFeedbackStore) FinalizeCollectingBatch(ctx context.Context, orgID, batchID uuid.UUID, now time.Time, botCycleLimit *int) (*models.PullRequestFeedbackBatch, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin finalize PR feedback collection: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT `+prFeedbackBatchColumns+` FROM pull_request_feedback_batches WHERE org_id=$1 AND id=$2 AND status='collecting' FOR UPDATE`, orgID, batchID)
	if err != nil {
		return nil, fmt.Errorf("lock collecting PR feedback batch: %w", err)
	}
	batch, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PullRequestFeedbackBatch])
	if err != nil {
		return nil, err
	}
	if now.Before(batch.DebounceUntil) && now.Before(batch.MaxCollectUntil) {
		return &batch, nil
	}
	var recentBatches, recentItems int
	if err := tx.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM pull_request_feedback_batches WHERE org_id=$1 AND pull_request_id=$2 AND created_at>=$3), (SELECT COUNT(*) FROM pull_request_feedback_items WHERE org_id=$1 AND pull_request_id=$2 AND received_at>=$3)`, orgID, batch.PullRequestID, now.Add(-time.Hour)).Scan(&recentBatches, &recentItems); err != nil {
		return nil, fmt.Errorf("check PR feedback hourly caps: %w", err)
	}
	if recentBatches > 5 || recentItems > 30 {
		return s.finishCollectionWithAttention(ctx, tx, batch, "hourly_cap_exhausted")
	}
	itemRows, err := tx.Query(ctx, `SELECT `+prFeedbackItemColumns+` FROM pull_request_feedback_items WHERE org_id=$1 AND pull_request_id=$2 AND status='pending' AND batch_id IS NULL ORDER BY github_review_id NULLS LAST,received_at,id FOR UPDATE SKIP LOCKED`, orgID, batch.PullRequestID)
	if err != nil {
		return nil, fmt.Errorf("select eligible PR feedback batch items: %w", err)
	}
	items, err := pgx.CollectRows(itemRows, pgx.RowToStructByName[models.PullRequestFeedbackItem])
	if err != nil {
		return nil, fmt.Errorf("collect eligible PR feedback batch items: %w", err)
	}
	if len(items) == 0 {
		return s.finishCollectionWithAttention(ctx, tx, batch, "no_eligible_items")
	}
	source := models.PRFeedbackBatchSourceBotOnly
	for _, item := range items {
		if item.AuthorType != models.PRFeedbackAuthorTypeBot {
			source = models.PRFeedbackBatchSourceHumanOrMixed
			break
		}
	}
	var epoch int64
	if source == models.PRFeedbackBatchSourceHumanOrMixed {
		if err := tx.QueryRow(ctx, `UPDATE pull_requests SET feedback_bot_epoch=feedback_bot_epoch+1,feedback_bot_cycles_in_epoch=0,updated_at=now() WHERE org_id=$1 AND id=$2 RETURNING feedback_bot_epoch`, orgID, batch.PullRequestID).Scan(&epoch); err != nil {
			return nil, fmt.Errorf("reset PR feedback bot epoch: %w", err)
		}
	} else {
		var cycles int
		if err := tx.QueryRow(ctx, `SELECT feedback_bot_epoch,feedback_bot_cycles_in_epoch FROM pull_requests WHERE org_id=$1 AND id=$2 FOR UPDATE`, orgID, batch.PullRequestID).Scan(&epoch, &cycles); err != nil {
			return nil, fmt.Errorf("load PR feedback bot budget: %w", err)
		}
		if botCycleLimit != nil && cycles >= *botCycleLimit {
			return s.finishCollectionWithAttention(ctx, tx, batch, "bot_cycle_limit_exhausted")
		}
		if _, err := tx.Exec(ctx, `UPDATE pull_requests SET feedback_bot_cycles_in_epoch=feedback_bot_cycles_in_epoch+1,updated_at=now() WHERE org_id=$1 AND id=$2`, orgID, batch.PullRequestID); err != nil {
			return nil, fmt.Errorf("consume PR feedback bot cycle: %w", err)
		}
	}
	snapshot, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("snapshot collected PR feedback: %w", err)
	}
	ids := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	if _, err := tx.Exec(ctx, `UPDATE pull_request_feedback_items SET batch_id=$1,status='claimed',automatic_attempt_count=automatic_attempt_count+1,updated_at=now() WHERE org_id=$2 AND id=ANY($3) AND status='pending' AND batch_id IS NULL`, batch.ID, orgID, ids); err != nil {
		return nil, fmt.Errorf("claim collected PR feedback items: %w", err)
	}
	rows, err = tx.Query(ctx, `UPDATE pull_request_feedback_batches SET status='queued',source_kind=$1,bot_feedback_epoch=$2,feedback_snapshot=$3,updated_at=now() WHERE org_id=$4 AND id=$5 AND status='collecting' RETURNING `+prFeedbackBatchColumns, source, epoch, snapshot, orgID, batch.ID)
	if err != nil {
		return nil, fmt.Errorf("queue collected PR feedback batch: %w", err)
	}
	batch, err = pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PullRequestFeedbackBatch])
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit finalized PR feedback collection: %w", err)
	}
	return &batch, nil
}

// QueueContinuation creates or reuses the PR's canonical feedback thread,
// persists the visible summary and durable inbox entry, advances the batch,
// and enqueues exactly one continuation in a single transaction.
func (s *PullRequestFeedbackStore) QueueContinuation(ctx context.Context, orgID, batchID uuid.UUID, visibleSummary, structuredPrompt string) (*models.PullRequestFeedbackBatch, error) {
	if s.jobs == nil {
		return nil, fmt.Errorf("PR feedback job store is not configured")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin PR feedback continuation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT `+prFeedbackBatchColumns+` FROM pull_request_feedback_batches WHERE org_id=$1 AND id=$2 FOR UPDATE`, orgID, batchID)
	if err != nil {
		return nil, fmt.Errorf("lock PR feedback continuation batch: %w", err)
	}
	batch, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PullRequestFeedbackBatch])
	if err != nil {
		return nil, err
	}
	if batch.Status != models.PRFeedbackBatchStatusQueued {
		return &batch, nil
	}
	var agentType models.AgentType
	var pullRequestNumber int
	if err := tx.QueryRow(ctx, `SELECT s.agent_type,p.github_pr_number FROM sessions s JOIN pull_requests p ON p.session_id=s.id AND p.org_id=s.org_id WHERE s.org_id=$1 AND s.id=$2 AND s.archived_at IS NULL AND p.id=$3 AND p.status='open' FOR UPDATE OF s,p`, orgID, batch.SessionID, batch.PullRequestID).Scan(&agentType, &pullRequestNumber); err != nil {
		return nil, fmt.Errorf("validate PR feedback session ownership: %w", err)
	}
	threadID := uuid.Nil
	if batch.ThreadID != nil {
		threadID = *batch.ThreadID
	} else {
		err := tx.QueryRow(ctx, `SELECT b.thread_id FROM pull_request_feedback_batches b JOIN session_threads t ON t.id=b.thread_id AND t.org_id=b.org_id AND t.session_id=b.session_id WHERE b.org_id=$1 AND b.pull_request_id=$2 AND b.thread_id IS NOT NULL AND t.archived_at IS NULL ORDER BY b.created_at DESC LIMIT 1`, orgID, batch.PullRequestID).Scan(&threadID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("find canonical PR feedback thread: %w", err)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			instructions := "Follow through on eligible feedback for this pull request only. Do not merge, approve, dismiss, resolve threads, push, post comments, access secrets, contact third parties, or modify another repository. Make and test local code changes, then summarize the result."
			err = tx.QueryRow(ctx, `INSERT INTO session_threads (session_id,org_id,agent_type,label,instructions,file_scope,status,created_by_source,execution_mode,filesystem_mode) VALUES ($1,$2,$3,'PR feedback',$4,'{}','pending','system','work','read_write') RETURNING id`, batch.SessionID, orgID, agentType, instructions).Scan(&threadID)
			if err != nil {
				return nil, fmt.Errorf("create canonical PR feedback thread: %w", err)
			}
		}
	}
	var currentTurn int
	if err := tx.QueryRow(ctx, `SELECT current_turn FROM session_threads WHERE org_id=$1 AND session_id=$2 AND id=$3 FOR UPDATE`, orgID, batch.SessionID, threadID).Scan(&currentTurn); err != nil {
		return nil, fmt.Errorf("lock canonical PR feedback thread: %w", err)
	}
	message := &models.SessionMessage{SessionID: batch.SessionID, OrgID: orgID, ThreadID: &threadID, TurnNumber: currentTurn + 1, Role: models.MessageRoleUser, Content: visibleSummary, Source: models.SessionMessageSourceGitHubPRFeedback}
	if err := NewSessionMessageStore(tx).CreateWithSource(ctx, message); err != nil {
		return nil, fmt.Errorf("create PR feedback session message: %w", err)
	}
	inboxPayload, err := json.Marshal(map[string]any{"content": visibleSummary, "feedback_batch_id": batch.ID, "structured_prompt": structuredPrompt})
	if err != nil {
		return nil, fmt.Errorf("marshal PR feedback inbox payload: %w", err)
	}
	clientMessageID := "pr-feedback:" + batch.ID.String()
	if _, err := NewThreadInboxStore(tx).AppendForMessage(ctx, orgID, AppendThreadInboxEntryParams{SessionID: batch.SessionID, ThreadID: threadID, MessageID: message.ID, ClientMessageID: clientMessageID, EntryType: models.ThreadInboxEntryTypeUserMessage, Payload: inboxPayload}); err != nil {
		return nil, fmt.Errorf("append PR feedback thread inbox: %w", err)
	}
	payload := map[string]any{"org_id": orgID.String(), "session_id": batch.SessionID.String(), "thread_id": threadID.String(), "queued_message_id": strconv.FormatInt(message.ID, 10), "feedback_batch_id": batch.ID.String(), "pull_request_id": batch.PullRequestID.String(), "pull_request_number": pullRequestNumber, "head_sha": batch.ExpectedHeadSHA, "workspace_mode": models.PRFeedbackWorkspaceModePRHeadReconstruction, "structured_prompt": structuredPrompt}
	dedupeKey := "continue_pr_feedback:" + batch.ID.String()
	jobID, err := s.jobs.EnqueueInTx(ctx, tx, orgID, "agent", "continue_session", payload, 5, &dedupeKey)
	if err != nil {
		return nil, fmt.Errorf("enqueue PR feedback continuation: %w", err)
	}
	rows, err = tx.Query(ctx, `UPDATE pull_request_feedback_batches SET thread_id=$1,status='running',workspace_mode='pr_head_reconstruction',started_at=COALESCE(started_at,now()),updated_at=now() WHERE org_id=$2 AND id=$3 AND status='queued' RETURNING `+prFeedbackBatchColumns, threadID, orgID, batch.ID)
	if err != nil {
		return nil, fmt.Errorf("start PR feedback continuation batch: %w", err)
	}
	batch, err = pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PullRequestFeedbackBatch])
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit PR feedback continuation: %w", err)
	}
	s.jobs.Notify(ctx, jobID)
	return &batch, nil
}

func (s *PullRequestFeedbackStore) finishCollectionWithAttention(ctx context.Context, tx pgx.Tx, batch models.PullRequestFeedbackBatch, code string) (*models.PullRequestFeedbackBatch, error) {
	rows, err := tx.Query(ctx, `UPDATE pull_request_feedback_batches SET status='needs_attention',error_code=$1,completed_at=now(),updated_at=now() WHERE org_id=$2 AND id=$3 AND status='collecting' RETURNING `+prFeedbackBatchColumns, code, batch.OrgID, batch.ID)
	if err != nil {
		return nil, fmt.Errorf("stop PR feedback collection: %w", err)
	}
	batch, err = pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PullRequestFeedbackBatch])
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit stopped PR feedback collection: %w", err)
	}
	return &batch, nil
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

func (s *PullRequestFeedbackStore) GetBatch(ctx context.Context, orgID, batchID uuid.UUID) (*models.PullRequestFeedbackBatch, error) {
	rows, err := s.db.Query(ctx, `SELECT `+prFeedbackBatchColumns+` FROM pull_request_feedback_batches WHERE org_id=$1 AND id=$2`, orgID, batchID)
	if err != nil {
		return nil, fmt.Errorf("get PR feedback batch: %w", err)
	}
	batch, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PullRequestFeedbackBatch])
	if err != nil {
		return nil, fmt.Errorf("collect PR feedback batch: %w", err)
	}
	return &batch, nil
}

func (s *PullRequestFeedbackStore) GetActiveBatch(ctx context.Context, orgID, pullRequestID uuid.UUID) (*models.PullRequestFeedbackBatch, error) {
	rows, err := s.db.Query(ctx, `SELECT `+prFeedbackBatchColumns+` FROM pull_request_feedback_batches WHERE org_id=$1 AND pull_request_id=$2 AND status IN ('collecting','queued','running','pushing','responding') ORDER BY created_at DESC LIMIT 1`, orgID, pullRequestID)
	if err != nil {
		return nil, fmt.Errorf("get active PR feedback batch: %w", err)
	}
	batch, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.PullRequestFeedbackBatch])
	if err != nil {
		return nil, fmt.Errorf("collect active PR feedback batch: %w", err)
	}
	return &batch, nil
}

func (s *PullRequestFeedbackStore) ListBatchItems(ctx context.Context, orgID, batchID uuid.UUID) ([]models.PullRequestFeedbackItem, error) {
	rows, err := s.db.Query(ctx, `SELECT `+prFeedbackItemColumns+` FROM pull_request_feedback_items WHERE org_id=$1 AND batch_id=$2 ORDER BY received_at,id`, orgID, batchID)
	if err != nil {
		return nil, fmt.Errorf("list PR feedback batch items: %w", err)
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.PullRequestFeedbackItem])
	if err != nil {
		return nil, fmt.Errorf("collect PR feedback batch items: %w", err)
	}
	return items, nil
}

func (s *PullRequestFeedbackStore) ListRecentItems(ctx context.Context, orgID, pullRequestID uuid.UUID, limit int) ([]models.PullRequestFeedbackItem, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.Query(ctx, `SELECT `+prFeedbackItemColumns+` FROM pull_request_feedback_items WHERE org_id=$1 AND pull_request_id=$2 ORDER BY received_at DESC,id DESC LIMIT $3`, orgID, pullRequestID, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent PR feedback items: %w", err)
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.PullRequestFeedbackItem])
	if err != nil {
		return nil, fmt.Errorf("collect recent PR feedback items: %w", err)
	}
	return items, nil
}

func (s *PullRequestFeedbackStore) ListPendingItems(ctx context.Context, orgID, pullRequestID uuid.UUID, limit int) ([]models.PullRequestFeedbackItem, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `SELECT `+prFeedbackItemColumns+` FROM pull_request_feedback_items WHERE org_id=$1 AND pull_request_id=$2 AND status='pending' AND batch_id IS NULL ORDER BY received_at,id LIMIT $3`, orgID, pullRequestID, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending PR feedback items: %w", err)
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.PullRequestFeedbackItem])
	if err != nil {
		return nil, fmt.Errorf("collect pending PR feedback items: %w", err)
	}
	return items, nil
}

func (s *PullRequestFeedbackStore) HasBotFingerprintOnHead(ctx context.Context, orgID, pullRequestID, excludeItemID uuid.UUID, fingerprint, headSHA string) (bool, error) {
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pull_request_feedback_items WHERE org_id=$1 AND pull_request_id=$2 AND id<>$3 AND author_type='Bot' AND finding_fingerprint=$4 AND observed_head_sha=$5 AND status IN ('claimed','running','responded','ignored','needs_attention'))`, orgID, pullRequestID, excludeItemID, fingerprint, headSHA).Scan(&exists); err != nil {
		return false, fmt.Errorf("check PR feedback bot fingerprint: %w", err)
	}
	return exists, nil
}

func (s *PullRequestFeedbackStore) CancelPendingThread(ctx context.Context, orgID, pullRequestID uuid.UUID, rootCommentID int64) (int64, error) {
	result, err := s.db.Exec(ctx, `UPDATE pull_request_feedback_items SET status='cancelled',ignore_reason='thread_resolved',processed_body_hash=body_hash,processed_at=now(),updated_at=now() WHERE org_id=$1 AND pull_request_id=$2 AND github_thread_root_comment_id=$3 AND status IN ('pending','claimed')`, orgID, pullRequestID, rootCommentID)
	if err != nil {
		return 0, fmt.Errorf("cancel resolved PR feedback thread: %w", err)
	}
	return result.RowsAffected(), nil
}

func (s *PullRequestFeedbackStore) CancelPendingThreadWithDelivery(ctx context.Context, delivery *models.WebhookDelivery, pullRequestID uuid.UUID, rootCommentID int64) (int64, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin resolved PR feedback thread: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var deliveryRowID uuid.UUID
	err = tx.QueryRow(ctx, `INSERT INTO webhook_deliveries (org_id,integration_id,provider,delivery_id,event_type,signature_valid,payload,headers,status) VALUES ($1,$2,'github',$3,$4,true,$5,$6,'processed') ON CONFLICT (provider,delivery_id) WHERE delivery_id IS NOT NULL DO NOTHING RETURNING id`, delivery.OrgID, delivery.IntegrationID, delivery.DeliveryID, delivery.EventType, delivery.Payload, delivery.Headers).Scan(&deliveryRowID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("record resolved PR feedback delivery: %w", err)
	}
	result, err := tx.Exec(ctx, `UPDATE pull_request_feedback_items SET status='cancelled',ignore_reason='thread_resolved',processed_body_hash=body_hash,processed_at=now(),updated_at=now() WHERE org_id=$1 AND pull_request_id=$2 AND github_thread_root_comment_id=$3 AND status IN ('pending','claimed')`, delivery.OrgID, pullRequestID, rootCommentID)
	if err != nil {
		return 0, fmt.Errorf("cancel resolved PR feedback thread: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit resolved PR feedback thread: %w", err)
	}
	delivery.ID = deliveryRowID
	return result.RowsAffected(), nil
}

func (s *PullRequestFeedbackStore) CountItemsByState(ctx context.Context, orgID, pullRequestID uuid.UUID) (pending, needsAttention int, err error) {
	err = s.db.QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE status='pending'), COUNT(*) FILTER (WHERE status='needs_attention') FROM pull_request_feedback_items WHERE org_id=$1 AND pull_request_id=$2`, orgID, pullRequestID).Scan(&pending, &needsAttention)
	if err != nil {
		return 0, 0, fmt.Errorf("count PR feedback items: %w", err)
	}
	return pending, needsAttention, nil
}

func (s *PullRequestFeedbackStore) SetBatchExecution(ctx context.Context, orgID, batchID, threadID uuid.UUID, mode models.PRFeedbackWorkspaceMode) error {
	if err := mode.Validate(); err != nil {
		return err
	}
	res, err := s.db.Exec(ctx, `UPDATE pull_request_feedback_batches SET thread_id=$1,workspace_mode=$2,updated_at=now() WHERE org_id=$3 AND id=$4 AND status IN ('queued','running')`, threadID, mode, orgID, batchID)
	if err != nil {
		return fmt.Errorf("set PR feedback batch execution: %w", err)
	}
	if res.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *PullRequestFeedbackStore) CompleteAgent(ctx context.Context, orgID, batchID uuid.UUID, summary string, resultHeadSHA *string, next models.PRFeedbackBatchStatus) (bool, error) {
	if next != models.PRFeedbackBatchStatusPushing && next != models.PRFeedbackBatchStatusResponding && next != models.PRFeedbackBatchStatusNeedsAttention {
		return false, fmt.Errorf("invalid post-agent PR feedback status: %q", next)
	}
	res, err := s.db.Exec(ctx, `UPDATE pull_request_feedback_batches SET status=$1,result_summary=$2,result_head_sha=$3,updated_at=now(),completed_at=CASE WHEN $1='needs_attention' THEN now() ELSE completed_at END WHERE org_id=$4 AND id=$5 AND status='running'`, next, summary, resultHeadSHA, orgID, batchID)
	if err != nil {
		return false, fmt.Errorf("complete PR feedback agent: %w", err)
	}
	return res.RowsAffected() == 1, nil
}

func (s *PullRequestFeedbackStore) RecordResponse(ctx context.Context, orgID, itemID uuid.UUID, commentID int64, body string, commitSHA *string) (bool, error) {
	res, err := s.db.Exec(ctx, `UPDATE pull_request_feedback_items SET status='responded',github_response_comment_id=$1,response_body=$2,response_commit_sha=$3,processed_body_hash=body_hash,processed_at=now(),updated_at=now() WHERE org_id=$4 AND id=$5 AND github_response_comment_id IS NULL AND status IN ('claimed','running','needs_attention')`, commentID, body, commitSHA, orgID, itemID)
	if err != nil {
		return false, fmt.Errorf("record PR feedback response: %w", err)
	}
	return res.RowsAffected() == 1, nil
}

func (s *PullRequestFeedbackStore) SetItemDecision(ctx context.Context, orgID, itemID uuid.UUID, intent models.PRFeedbackIntent, status models.PRFeedbackItemStatus, ignoreReason *string, fingerprint *string, eligibility models.PRFeedbackBotEligibilitySource) error {
	if err := intent.Validate(); err != nil {
		return err
	}
	if err := status.Validate(); err != nil {
		return err
	}
	res, err := s.db.Exec(ctx, `UPDATE pull_request_feedback_items SET intent=$1,status=$2,ignore_reason=$3,finding_fingerprint=$4,bot_eligibility_source=$5,processed_body_hash=CASE WHEN $2='ignored' THEN body_hash ELSE processed_body_hash END,processed_at=CASE WHEN $2='ignored' THEN now() ELSE processed_at END,updated_at=now() WHERE org_id=$6 AND id=$7`, intent, status, ignoreReason, fingerprint, eligibility, orgID, itemID)
	if err != nil {
		return fmt.Errorf("set PR feedback item decision: %w", err)
	}
	if res.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *PullRequestFeedbackStore) UpdateMonitoring(ctx context.Context, orgID, pullRequestID uuid.UUID, monitoring models.PRFeedbackMonitoring) error {
	if err := monitoring.Validate(); err != nil {
		return err
	}
	res, err := s.db.Exec(ctx, `UPDATE pull_requests SET feedback_monitoring=$1,updated_at=now() WHERE org_id=$2 AND id=$3`, monitoring, orgID, pullRequestID)
	if err != nil {
		return fmt.Errorf("update PR feedback monitoring: %w", err)
	}
	if res.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

// RetryBatch returns a needs-attention batch to queued state against the
// caller-verified current head and resets the bot epoch as an explicit human
// intervention. The active-batch partial index prevents a retry from creating
// a second writer for the PR.
func (s *PullRequestFeedbackStore) RetryBatch(ctx context.Context, orgID, pullRequestID, batchID uuid.UUID, headSHA string) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin PR feedback retry: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	res, err := tx.Exec(ctx, `UPDATE pull_requests SET feedback_bot_epoch=feedback_bot_epoch+1,feedback_bot_cycles_in_epoch=0,updated_at=now() WHERE org_id=$1 AND id=$2`, orgID, pullRequestID)
	if err != nil {
		return false, fmt.Errorf("reset PR feedback bot epoch: %w", err)
	}
	if res.RowsAffected() != 1 {
		return false, pgx.ErrNoRows
	}
	res, err = tx.Exec(ctx, `UPDATE pull_request_feedback_batches SET status='queued',expected_head_sha=$1,error_code=NULL,error_detail=NULL,completed_at=NULL,updated_at=now() WHERE org_id=$2 AND pull_request_id=$3 AND id=$4 AND status='needs_attention'`, headSHA, orgID, pullRequestID, batchID)
	if err != nil {
		return false, fmt.Errorf("retry PR feedback batch: %w", err)
	}
	if res.RowsAffected() != 1 {
		return false, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit PR feedback retry: %w", err)
	}
	return true, nil
}
