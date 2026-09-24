package db

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrCodeReviewRecheckFence = errors.New("code review recheck turn lost its assessment or job lease")
var ErrCodeReviewRecheckUserCancelled = errors.New("code review recheck turn was cancelled by the user")

// RejectIfCodeReviewOwned blocks ordinary conversation mutations after a
// review has reserved its orchestrator session. It is deliberately separate
// from the assessment lifecycle: completed reviews retain the conversation.
func (s *SessionStore) RejectIfCodeReviewOwned(ctx context.Context, orgID, sessionID uuid.UUID) error {
	var prID *uuid.UUID
	err := s.db.QueryRow(ctx, `SELECT code_review_owner_pr_id FROM sessions WHERE org_id=$1 AND id=$2`, orgID, sessionID).Scan(&prID)
	if err != nil {
		return err
	}
	if prID != nil {
		return &models.SessionCodeReviewOwnedError{PullRequestID: *prID}
	}
	return nil
}

type CodeReviewRecheckStore struct {
	db   TxStarter
	jobs *JobStore
}

func NewCodeReviewRecheckStore(conn TxStarter) *CodeReviewRecheckStore {
	return &CodeReviewRecheckStore{db: conn, jobs: NewJobStore(conn)}
}

// ClaimFullAssessmentOwner reserves the completed full review's conversation
// for the PR before coverage is marked reusable. The caller must invoke this
// in the same transaction as the assessment completion. A terminal status by
// itself does not prove that a sandbox, preview, or executor has drained.
func (s *CodeReviewRecheckStore) ClaimFullAssessmentOwner(ctx context.Context, tx pgx.Tx, orgID, sessionID, pullRequestID uuid.UUID) (bool, error) {
	if orgID == uuid.Nil || sessionID == uuid.Nil || pullRequestID == uuid.Nil || tx == nil {
		return false, fmt.Errorf("invalid full assessment conversation owner")
	}
	var claimed uuid.UUID
	err := tx.QueryRow(ctx, `UPDATE sessions s SET code_review_owner_pr_id=$3
		WHERE s.org_id=$1 AND s.id=$2 AND s.origin='code_review'
		AND s.status IN ('idle','completed','running')
		AND EXISTS (SELECT 1 FROM code_review_session_metadata m WHERE m.org_id=s.org_id AND m.session_id=s.id AND m.pull_request_id=$3 AND m.status='completed')
		AND (s.code_review_owner_pr_id IS NULL OR s.code_review_owner_pr_id=$3)
		AND s.container_id IS NULL AND NOT COALESCE(s.turn_holding_container,false)
		AND NOT EXISTS (SELECT 1 FROM preview_instances p WHERE p.org_id=s.org_id AND p.session_id=s.id AND p.preview_holding_container)
		AND NOT EXISTS (SELECT 1 FROM thread_runtimes r WHERE r.org_id=s.org_id AND r.session_id=s.id AND r.status IN ('starting','live','paused','draining'))
		AND NOT EXISTS (SELECT 1 FROM session_executors e WHERE e.org_id=s.org_id AND e.session_id=s.id AND e.status IN ('starting','running','draining'))
		AND NOT EXISTS (SELECT 1 FROM session_threads t WHERE t.org_id=s.org_id AND t.session_id=s.id AND t.status NOT IN ('idle','completed','cancelled','failed'))
		RETURNING s.id`, orgID, sessionID, pullRequestID).Scan(&claimed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// RetireOwnerForCompletedReplacement releases the old conversation only after
// a newer full assessment has completed in another session. The PR lock makes
// this mutually exclusive with admission/dispatch. A still-owned runtime or
// active assessment keeps the old conversation protected for later recovery.
func (s *CodeReviewRecheckStore) RetireOwnerForCompletedReplacement(ctx context.Context, tx pgx.Tx, orgID, oldSessionID, pullRequestID, replacementAssessmentID uuid.UUID) (bool, error) {
	if orgID == uuid.Nil || oldSessionID == uuid.Nil || pullRequestID == uuid.Nil || replacementAssessmentID == uuid.Nil || tx == nil {
		return false, fmt.Errorf("invalid retired code review conversation")
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "code_review_pr:"+orgID.String()+":"+pullRequestID.String()); err != nil {
		return false, err
	}
	var released uuid.UUID
	err := tx.QueryRow(ctx, `UPDATE sessions s SET code_review_owner_pr_id=NULL
		WHERE s.org_id=$1 AND s.id=$2 AND s.code_review_owner_pr_id=$3
		AND s.status IN ('idle','completed') AND s.container_id IS NULL AND NOT COALESCE(s.turn_holding_container,false)
		AND EXISTS (SELECT 1 FROM code_review_revision_assessments replacement
			WHERE replacement.org_id=s.org_id AND replacement.id=$4 AND replacement.pull_request_id=$3
			AND replacement.session_id<>s.id AND replacement.review_scope='full' AND replacement.status='completed'
			AND replacement.generation > (SELECT MAX(prior.generation) FROM code_review_revision_assessments prior
				WHERE prior.org_id=s.org_id AND prior.pull_request_id=$3 AND prior.session_id=s.id))
		AND NOT EXISTS (SELECT 1 FROM code_review_revision_assessments active
			WHERE active.org_id=s.org_id AND active.session_id=s.id AND active.pull_request_id=$3 AND active.status IN ('reserved','running','publishing'))
		AND NOT EXISTS (SELECT 1 FROM code_review_recheck_dispatches d
			WHERE d.org_id=s.org_id AND d.session_id=s.id AND d.status IN ('pending','running'))
		AND NOT EXISTS (SELECT 1 FROM preview_instances p WHERE p.org_id=s.org_id AND p.session_id=s.id AND p.preview_holding_container)
		AND NOT EXISTS (SELECT 1 FROM thread_runtimes r WHERE r.org_id=s.org_id AND r.session_id=s.id AND r.status IN ('starting','live','paused','draining'))
		AND NOT EXISTS (SELECT 1 FROM session_executors e WHERE e.org_id=s.org_id AND e.session_id=s.id AND e.status IN ('starting','running','draining'))
		AND NOT EXISTS (SELECT 1 FROM session_threads t WHERE t.org_id=s.org_id AND t.session_id=s.id AND t.status NOT IN ('idle','completed','cancelled','failed'))
		RETURNING s.id`, orgID, oldSessionID, pullRequestID, replacementAssessmentID).Scan(&released)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// SetJobStore supplies the worker's notifier. Notify is sent only after the
// dispatch transaction commits; the database row is the durable wake source.
// lint:allow-no-orgid reason="process-wide dependency injection for queue notification"
func (s *CodeReviewRecheckStore) SetJobStore(jobs *JobStore) { s.jobs = jobs }

const recheckDispatchColumns = `assessment_id,org_id,repository_id,pull_request_id,session_id,thread_id,expected_turn,payload_digest,message_id,job_id,status,attempt_lock_token,result_message_id,provider_session_id,snapshot_key,native_context,failure_detail,attempt_usage,completed_at,created_at,updated_at`

func (s *CodeReviewRecheckStore) Get(ctx context.Context, orgID, assessmentID uuid.UUID) (models.CodeReviewRecheckDispatch, error) {
	rows, err := s.db.Query(ctx, `SELECT `+recheckDispatchColumns+` FROM code_review_recheck_dispatches WHERE org_id=$1 AND assessment_id=$2`, orgID, assessmentID)
	if err != nil {
		return models.CodeReviewRecheckDispatch{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CodeReviewRecheckDispatch])
}

// Dispatch atomically binds the assessment, exact user message, inbox entry,
// and one assessment-keyed continue_session job. A retry returns the existing
// outbox row without sending again. The caller supplies the already rendered,
// bounded prompt; admission and PR input comparison happen before this method.
func (s *CodeReviewRecheckStore) Dispatch(ctx context.Context, in models.CodeReviewRecheckDispatchInput) (models.CodeReviewRecheckDispatch, bool, error) {
	if in.OrgID == uuid.Nil || in.RepositoryID == uuid.Nil || in.PullRequestID == uuid.Nil || in.AssessmentID == uuid.Nil || in.SessionID == uuid.Nil || in.ThreadID == uuid.Nil || in.ExpectedTurn < 1 || strings.TrimSpace(in.Prompt) == "" || len(in.Prompt) > 256*1024 || len(in.ImageURLs) > models.CodeReviewVisualEvidenceMaxImages {
		return models.CodeReviewRecheckDispatch{}, false, fmt.Errorf("invalid code review recheck dispatch identity or prompt")
	}
	for _, imageURL := range in.ImageURLs {
		if imageURL == "" || len(imageURL) > 4096 {
			return models.CodeReviewRecheckDispatch{}, false, fmt.Errorf("invalid code review recheck image URL")
		}
	}
	inputBytes := len(in.Prompt)
	for _, imageURL := range in.ImageURLs {
		inputBytes += len(imageURL)
	}
	if inputBytes > 256*1024 {
		return models.CodeReviewRecheckDispatch{}, false, fmt.Errorf("code review recheck message and image URLs exceed 256 KiB")
	}
	payloadJSON, err := json.Marshal(struct {
		Prompt    string   `json:"prompt"`
		ImageURLs []string `json:"image_urls"`
	}{in.Prompt, in.ImageURLs})
	if err != nil {
		return models.CodeReviewRecheckDispatch{}, false, err
	}
	payloadDigest := fmt.Sprintf("%x", sha256.Sum256(payloadJSON))
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return models.CodeReviewRecheckDispatch{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "code_review_pr:"+in.OrgID.String()+":"+in.PullRequestID.String()); err != nil {
		return models.CodeReviewRecheckDispatch{}, false, err
	}
	var assessmentSession uuid.UUID
	var scope models.CodeReviewScope
	var status models.CodeReviewAssessmentStatus
	err = tx.QueryRow(ctx, `SELECT session_id,review_scope,status FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2 AND repository_id=$3 AND pull_request_id=$4 FOR UPDATE`, in.OrgID, in.AssessmentID, in.RepositoryID, in.PullRequestID).Scan(&assessmentSession, &scope, &status)
	if err != nil {
		return models.CodeReviewRecheckDispatch{}, false, err
	}
	if assessmentSession != in.SessionID || scope != models.CodeReviewScopeEvidenceOnly {
		return models.CodeReviewRecheckDispatch{}, false, ErrCodeReviewRecheckFence
	}
	rows, err := tx.Query(ctx, `SELECT `+recheckDispatchColumns+` FROM code_review_recheck_dispatches WHERE org_id=$1 AND assessment_id=$2`, in.OrgID, in.AssessmentID)
	if err != nil {
		return models.CodeReviewRecheckDispatch{}, false, err
	}
	prior, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.CodeReviewRecheckDispatch])
	if err != nil {
		return models.CodeReviewRecheckDispatch{}, false, err
	}
	if len(prior) > 0 {
		d := prior[0]
		if d.SessionID != in.SessionID || d.ThreadID != in.ThreadID || d.ExpectedTurn != in.ExpectedTurn || d.PayloadDigest != payloadDigest {
			return models.CodeReviewRecheckDispatch{}, false, ErrCodeReviewRecheckFence
		}
		if err = tx.Commit(ctx); err != nil {
			return models.CodeReviewRecheckDispatch{}, false, err
		}
		return d, true, nil
	}
	if status != models.CodeReviewAssessmentRunning {
		return models.CodeReviewRecheckDispatch{}, false, ErrCodeReviewRecheckFence
	}
	// The session row serializes review ownership with ordinary human claims.
	// A lease expiry alone is never proof that a sandbox or executor stopped.
	var ownerReady bool
	err = tx.QueryRow(ctx, `SELECT (s.origin='code_review' AND s.status IN ('idle','completed') AND (s.code_review_owner_pr_id IS NULL OR s.code_review_owner_pr_id=$3) AND s.container_id IS NULL AND NOT COALESCE(s.turn_holding_container,false) AND NOT EXISTS(SELECT 1 FROM preview_instances p WHERE p.org_id=s.org_id AND p.session_id=s.id AND p.preview_holding_container) AND NOT EXISTS(SELECT 1 FROM thread_runtimes r WHERE r.org_id=s.org_id AND r.session_id=s.id AND r.status IN ('starting','live','paused','draining')) AND NOT EXISTS(SELECT 1 FROM session_executors e WHERE e.org_id=s.org_id AND e.session_id=s.id AND e.status IN ('starting','running','draining'))) FROM sessions s WHERE s.org_id=$1 AND s.id=$2 FOR UPDATE`, in.OrgID, in.SessionID, in.PullRequestID).Scan(&ownerReady)
	if err != nil {
		return models.CodeReviewRecheckDispatch{}, false, err
	}
	if !ownerReady {
		return models.CodeReviewRecheckDispatch{}, false, ErrCodeReviewRecheckFence
	}
	if _, err = tx.Exec(ctx, `UPDATE sessions SET code_review_owner_pr_id=$3,status='running',started_at=now(),completed_at=NULL WHERE org_id=$1 AND id=$2 AND (code_review_owner_pr_id IS NULL OR code_review_owner_pr_id=$3)`, in.OrgID, in.SessionID, in.PullRequestID); err != nil {
		return models.CodeReviewRecheckDispatch{}, false, err
	}
	var currentTurn int
	var threadStatus models.ThreadStatus
	err = tx.QueryRow(ctx, `SELECT current_turn,status FROM session_threads WHERE org_id=$1 AND id=$2 AND session_id=$3 AND archived_at IS NULL FOR UPDATE`, in.OrgID, in.ThreadID, in.SessionID).Scan(&currentTurn, &threadStatus)
	if err != nil {
		return models.CodeReviewRecheckDispatch{}, false, err
	}
	if currentTurn+1 != in.ExpectedTurn || threadStatus != models.ThreadStatusIdle {
		return models.CodeReviewRecheckDispatch{}, false, ErrCodeReviewRecheckFence
	}
	// Another generic continuation on this thread would make the exact turn
	// ambiguous. The assessment job uses its own dedupe key intentionally.
	var genericActive bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE org_id=$1 AND queue='agent' AND dedupe_key=$2 AND status IN ('pending','running'))`, in.OrgID, ContinueSessionDedupeKey(in.ThreadID)).Scan(&genericActive)
	if err != nil {
		return models.CodeReviewRecheckDispatch{}, false, err
	}
	if genericActive {
		return models.CodeReviewRecheckDispatch{}, false, ErrCodeReviewRecheckFence
	}
	threadID := in.ThreadID
	msg := &models.SessionMessage{SessionID: in.SessionID, OrgID: in.OrgID, ThreadID: &threadID, TurnNumber: in.ExpectedTurn, Role: models.MessageRoleUser, Content: in.Prompt, Attachments: in.ImageURLs, Source: models.SessionMessageSourceCodeReviewRecheck}
	if err = NewSessionMessageStore(tx).CreateWithSource(ctx, msg); err != nil {
		return models.CodeReviewRecheckDispatch{}, false, err
	}
	inboxPayload, err := json.Marshal(map[string]any{"message_id": msg.ID, "turn_number": in.ExpectedTurn, "content": in.Prompt})
	if err != nil {
		return models.CodeReviewRecheckDispatch{}, false, err
	}
	_, err = NewThreadInboxStore(tx).AppendForMessage(ctx, in.OrgID, AppendThreadInboxEntryParams{SessionID: in.SessionID, ThreadID: in.ThreadID, MessageID: msg.ID, ClientMessageID: "code_review_recheck:" + in.AssessmentID.String(), EntryType: models.ThreadInboxEntryTypeUserMessage, Payload: inboxPayload})
	if err != nil {
		return models.CodeReviewRecheckDispatch{}, false, err
	}
	tag, err := tx.Exec(ctx, `UPDATE session_threads SET status='running',started_at=now(),completed_at=NULL,last_activity_at=now() WHERE org_id=$1 AND id=$2 AND session_id=$3 AND current_turn=$4 AND status='idle'`, in.OrgID, in.ThreadID, in.SessionID, in.ExpectedTurn-1)
	if err != nil {
		return models.CodeReviewRecheckDispatch{}, false, err
	}
	if tag.RowsAffected() != 1 {
		return models.CodeReviewRecheckDispatch{}, false, ErrCodeReviewRecheckFence
	}
	dedupe := "code_review_recheck_turn:" + in.AssessmentID.String()
	jobID, err := s.jobs.EnqueueInTxWithOpts(ctx, tx, in.OrgID, EnqueueOpts{Queue: "agent", JobType: "continue_session", Payload: map[string]string{"org_id": in.OrgID.String(), "session_id": in.SessionID.String(), "thread_id": in.ThreadID.String(), "queued_message_id": fmt.Sprint(msg.ID), "code_review_assessment_id": in.AssessmentID.String()}, DedupeKey: &dedupe, Priority: 5, MaxAttempts: 5})
	if err != nil {
		return models.CodeReviewRecheckDispatch{}, false, err
	}
	if jobID == uuid.Nil {
		return models.CodeReviewRecheckDispatch{}, false, fmt.Errorf("recheck enqueue collided without a dispatch receipt: %w", ErrCodeReviewRecheckFence)
	}
	rows, err = tx.Query(ctx, `INSERT INTO code_review_recheck_dispatches(assessment_id,org_id,repository_id,pull_request_id,session_id,thread_id,expected_turn,payload_digest,message_id,job_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING `+recheckDispatchColumns, in.AssessmentID, in.OrgID, in.RepositoryID, in.PullRequestID, in.SessionID, in.ThreadID, in.ExpectedTurn, payloadDigest, msg.ID, jobID)
	if err != nil {
		return models.CodeReviewRecheckDispatch{}, false, err
	}
	d, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CodeReviewRecheckDispatch])
	if err != nil {
		return models.CodeReviewRecheckDispatch{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return models.CodeReviewRecheckDispatch{}, false, err
	}
	s.jobs.Notify(context.WithoutCancel(ctx), jobID)
	return d, false, nil
}

// Claim checks the actual job lease before a provider is launched. A reclaimed
// job cannot borrow an earlier worker's completion authority.
func (s *CodeReviewRecheckStore) Claim(ctx context.Context, orgID, assessmentID, jobID, lockToken uuid.UUID, sessionID, threadID uuid.UUID, expectedTurn int, messageID int64) (bool, error) {
	if lockToken == uuid.Nil || jobID == uuid.Nil {
		return false, ErrCodeReviewRecheckFence
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id uuid.UUID
	var dispatchStatus models.CodeReviewRecheckDispatchStatus
	var priorToken *uuid.UUID
	err = tx.QueryRow(ctx, `SELECT d.assessment_id,d.status,d.attempt_lock_token FROM code_review_recheck_dispatches d JOIN jobs j ON j.org_id=d.org_id AND j.id=d.job_id JOIN code_review_revision_assessments a ON a.org_id=d.org_id AND a.id=d.assessment_id JOIN session_threads t ON t.org_id=d.org_id AND t.id=d.thread_id AND t.session_id=d.session_id JOIN session_messages m ON m.org_id=d.org_id AND m.id=d.message_id AND m.session_id=d.session_id AND m.thread_id=d.thread_id AND m.turn_number=d.expected_turn AND m.role='user' WHERE d.org_id=$1 AND d.assessment_id=$2 AND d.job_id=$3 AND d.session_id=$5 AND d.thread_id=$6 AND d.expected_turn=$7 AND d.message_id=$8 AND t.current_turn=d.expected_turn-1 AND t.status='running' AND t.archived_at IS NULL AND d.status IN ('pending','running') AND a.status='running' AND a.review_scope='evidence_only' AND j.status='running' AND j.lock_token=$4 FOR UPDATE OF j,d,a,t`, orgID, assessmentID, jobID, lockToken, sessionID, threadID, expectedTurn, messageID).Scan(&id, &dispatchStatus, &priorToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if dispatchStatus == models.CodeReviewRecheckDispatchRunning && (priorToken == nil || *priorToken != lockToken) {
		// A job lease timeout is insufficient: a prior provider may still own
		// the sandbox. Reclaim the *same* exact turn only after the session,
		// preview, thread runtime, and executor are all visibly drained.
		var drained bool
		err = tx.QueryRow(ctx, `SELECT (s.container_id IS NULL AND NOT COALESCE(s.turn_holding_container,false)
			AND NOT EXISTS(SELECT 1 FROM preview_instances p WHERE p.org_id=s.org_id AND p.session_id=s.id AND p.preview_holding_container)
			AND NOT EXISTS(SELECT 1 FROM thread_runtimes r WHERE r.org_id=s.org_id AND r.session_id=s.id AND r.status IN ('starting','live','paused','draining'))
			AND NOT EXISTS(SELECT 1 FROM session_executors e WHERE e.org_id=s.org_id AND e.session_id=s.id AND e.status IN ('starting','running','draining')))
			FROM sessions s WHERE s.org_id=$1 AND s.id=$2 AND s.code_review_owner_pr_id IS NOT NULL FOR UPDATE`, orgID, sessionID).Scan(&drained)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil || !drained {
			return false, err
		}
	}
	var cancelled bool
	err = tx.QueryRow(ctx, `SELECT (COALESCE(t.cancel_requested_at >= d.created_at,false) OR EXISTS(
		SELECT 1 FROM session_cancel_requests c WHERE c.org_id=d.org_id AND c.session_id=d.session_id AND c.requested_at >= d.created_at))
		FROM code_review_recheck_dispatches d JOIN session_threads t ON t.org_id=d.org_id AND t.id=d.thread_id
		WHERE d.org_id=$1 AND d.assessment_id=$2`, orgID, assessmentID).Scan(&cancelled)
	if err != nil {
		return false, err
	}
	if cancelled {
		if _, err = tx.Exec(ctx, `UPDATE code_review_recheck_dispatches SET status='cancelled',failure_detail='user cancelled recheck turn',completed_at=now(),updated_at=now() WHERE org_id=$1 AND assessment_id=$2 AND status IN ('pending','running')`, orgID, assessmentID); err != nil {
			return false, err
		}
		if _, err = tx.Exec(ctx, `UPDATE code_review_revision_assessments SET status='cancelled',failure_detail='user cancelled recheck turn',completed_at=now(),updated_at=now() WHERE org_id=$1 AND id=$2 AND status='running'`, orgID, assessmentID); err != nil {
			return false, err
		}
		var drained bool
		err = tx.QueryRow(ctx, `SELECT (s.container_id IS NULL AND NOT COALESCE(s.turn_holding_container,false)
			AND NOT EXISTS(SELECT 1 FROM preview_instances p WHERE p.org_id=s.org_id AND p.session_id=s.id AND p.preview_holding_container)
			AND NOT EXISTS(SELECT 1 FROM thread_runtimes r WHERE r.org_id=s.org_id AND r.session_id=s.id AND r.status IN ('starting','live','paused','draining'))
			AND NOT EXISTS(SELECT 1 FROM session_executors e WHERE e.org_id=s.org_id AND e.session_id=s.id AND e.status IN ('starting','running','draining')))
			FROM sessions s WHERE s.org_id=$1 AND s.id=$2 FOR UPDATE`, orgID, sessionID).Scan(&drained)
		if err != nil {
			return false, err
		}
		if drained {
			if _, err = tx.Exec(ctx, `UPDATE session_threads SET status='idle',current_turn=$4,completed_at=now(),last_activity_at=now() WHERE org_id=$1 AND id=$2 AND session_id=$3 AND current_turn=$4-1 AND status IN ('running','cancelled')`, orgID, threadID, sessionID, expectedTurn); err != nil {
				return false, err
			}
			if _, err = tx.Exec(ctx, `UPDATE sessions SET status='idle',last_activity_at=now() WHERE org_id=$1 AND id=$2 AND status IN ('running','cancelled')`, orgID, sessionID); err != nil {
				return false, err
			}
		}
		if err = tx.Commit(ctx); err != nil {
			return false, err
		}
		return false, ErrCodeReviewRecheckUserCancelled
	}
	tag, err := tx.Exec(ctx, `UPDATE code_review_recheck_dispatches SET status='running',attempt_lock_token=$3,updated_at=now() WHERE org_id=$1 AND assessment_id=$2 AND status IN ('pending','running')`, orgID, assessmentID, lockToken)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() != 1 {
		return false, ErrCodeReviewRecheckFence
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// NativeResumeProvider returns the exact previous thread provider only when
// its completed receipt still describes the installed session snapshot and
// the current assessment's immutable code and prompt contract. The first
// recheck has no such receipt and must reconstruct from bounded evidence.
func (s *CodeReviewRecheckStore) NativeResumeProvider(ctx context.Context, orgID, assessmentID, jobID, lockToken uuid.UUID) (string, bool, error) {
	var provider string
	err := s.db.QueryRow(ctx, `SELECT p.provider_session_id
		FROM code_review_recheck_dispatches d
		JOIN code_review_revision_assessments a ON a.org_id=d.org_id AND a.id=d.assessment_id
		JOIN code_review_recheck_dispatches p ON p.org_id=d.org_id AND p.thread_id=d.thread_id AND p.session_id=d.session_id AND p.pull_request_id=d.pull_request_id AND p.expected_turn=d.expected_turn-1
		JOIN code_review_revision_assessments pa ON pa.org_id=p.org_id AND pa.id=p.assessment_id
		JOIN sessions s ON s.org_id=d.org_id AND s.id=d.session_id
		JOIN session_threads t ON t.org_id=d.org_id AND t.id=d.thread_id AND t.session_id=d.session_id
		JOIN jobs j ON j.org_id=d.org_id AND j.id=d.job_id
		WHERE d.org_id=$1 AND d.assessment_id=$2 AND d.job_id=$3 AND d.attempt_lock_token=$4 AND d.status='running'
		AND j.status='running' AND j.lock_token=$4 AND a.status='running'
		AND a.input_digest IS NOT NULL AND pa.head_sha=a.head_sha AND pa.code_digest=a.code_digest AND pa.contract_digest=a.contract_digest
		AND p.status='completed' AND pa.status='completed' AND pa.result_origin='evidence_only' AND p.result_message_id IS NOT NULL AND NULLIF(p.provider_session_id,'') IS NOT NULL
		AND NULLIF(p.snapshot_key,'') IS NOT NULL AND s.snapshot_key=p.snapshot_key AND s.pending_snapshot_key IS NULL
		AND s.code_review_owner_pr_id=d.pull_request_id AND s.container_id IS NULL AND NOT COALESCE(s.turn_holding_container,false)
		AND NOT EXISTS(SELECT 1 FROM thread_runtimes r WHERE r.org_id=s.org_id AND r.session_id=s.id AND r.status IN ('starting','live','paused','draining'))
		AND NOT EXISTS(SELECT 1 FROM session_executors e WHERE e.org_id=s.org_id AND e.session_id=s.id AND e.status IN ('starting','running','draining'))
		AND t.current_turn=p.expected_turn AND t.status='running' AND t.agent_session_id=p.provider_session_id`, orgID, assessmentID, jobID, lockToken).Scan(&provider)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return provider, err == nil, err
}

// LockAttempt is used inside the completion transaction. Job and dispatch
// locks exclude a concurrent reclaim until the receipt commits.
func (s *CodeReviewRecheckStore) LockAttempt(ctx context.Context, tx pgx.Tx, orgID, assessmentID, jobID, lockToken uuid.UUID) (bool, error) {
	if lockToken == uuid.Nil {
		return false, ErrCodeReviewRecheckFence
	}
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT d.assessment_id FROM code_review_recheck_dispatches d JOIN jobs j ON j.org_id=d.org_id AND j.id=d.job_id JOIN code_review_revision_assessments a ON a.org_id=d.org_id AND a.id=d.assessment_id WHERE d.org_id=$1 AND d.assessment_id=$2 AND d.job_id=$3 AND d.attempt_lock_token=$4 AND d.status='running' AND a.status='running' AND a.review_scope='evidence_only' AND j.status='running' AND j.lock_token=$4 FOR UPDATE OF j,d,a`, orgID, assessmentID, jobID, lockToken).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Complete writes the exact assistant turn, session/thread terminal state, and
// receipt in one transaction under the job lease. A checkpoint is optional:
// a successful turn with no coherent checkpoint is still durable and can be
// reconstructed from transcript on the next assessment.
func (s *CodeReviewRecheckStore) Complete(ctx context.Context, c models.CodeReviewRecheckTurnCompletion) (int64, error) {
	if c.OrgID == uuid.Nil || c.AssessmentID == uuid.Nil || c.JobID == uuid.Nil || c.LockToken == uuid.Nil || c.SessionID == uuid.Nil || c.ThreadID == uuid.Nil || c.ExpectedTurn < 1 || c.SessionTurn < 1 || c.Result == nil || len(c.Summary) > 256*1024 || (len(c.TokenUsage) > 0 && !json.Valid(c.TokenUsage)) {
		return 0, fmt.Errorf("invalid code review recheck completion")
	}
	// A code review turn is read-only. Reusing the ordinary session writer for
	// a changed worktree would open its own diff transaction and invalidate the
	// one-transaction receipt guarantee.
	if c.Result.Diff != nil && *c.Result.Diff != "" {
		return 0, fmt.Errorf("code review recheck changed workspace")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	owned, err := s.LockAttempt(ctx, tx, c.OrgID, c.AssessmentID, c.JobID, c.LockToken)
	if err != nil {
		return 0, err
	}
	if !owned {
		return 0, ErrCodeReviewRecheckFence
	}
	var messageID int64
	var expectedTurn int
	var sessionID, threadID, pullRequestID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT message_id,expected_turn,session_id,thread_id,pull_request_id FROM code_review_recheck_dispatches WHERE org_id=$1 AND assessment_id=$2`, c.OrgID, c.AssessmentID).Scan(&messageID, &expectedTurn, &sessionID, &threadID, &pullRequestID)
	if err != nil {
		return 0, err
	}
	if expectedTurn != c.ExpectedTurn || sessionID != c.SessionID || threadID != c.ThreadID {
		return 0, ErrCodeReviewRecheckFence
	}
	var userMessageID int64
	err = tx.QueryRow(ctx, `SELECT id FROM session_messages WHERE org_id=$1 AND id=$2 AND session_id=$3 AND thread_id=$4 AND turn_number=$5 AND role='user'`, c.OrgID, messageID, c.SessionID, c.ThreadID, c.ExpectedTurn).Scan(&userMessageID)
	if err != nil {
		return 0, err
	}
	var threadTurn int
	err = tx.QueryRow(ctx, `SELECT current_turn FROM session_threads WHERE org_id=$1 AND id=$2 AND session_id=$3 AND status='running' FOR UPDATE`, c.OrgID, c.ThreadID, c.SessionID).Scan(&threadTurn)
	if err != nil {
		return 0, err
	}
	if threadTurn+1 != c.ExpectedTurn {
		return 0, ErrCodeReviewRecheckFence
	}
	// The shared root session's token_usage is the original full review's
	// historical cost. Per-recheck usage lives on this assistant message and
	// in attempt_usage; replacing the root value would make legacy readers
	// silently lose or double-count the baseline.
	var rootUsage json.RawMessage
	if err = tx.QueryRow(ctx, `SELECT token_usage FROM sessions WHERE org_id=$1 AND id=$2 AND code_review_owner_pr_id=$3 FOR UPDATE`, c.OrgID, c.SessionID, pullRequestID).Scan(&rootUsage); err != nil {
		return 0, err
	}
	id := c.ThreadID
	msg := &models.SessionMessage{OrgID: c.OrgID, SessionID: c.SessionID, ThreadID: &id, TurnNumber: c.ExpectedTurn, Role: models.MessageRoleAssistant, Content: c.Summary, TokenUsage: c.TokenUsage}
	if err = NewSessionMessageStore(tx).Create(ctx, msg); err != nil {
		return 0, err
	}
	rootResult := *c.Result
	rootResult.TokenUsage = rootUsage
	if err = NewSessionStore(tx).UpdateTurnComplete(ctx, c.OrgID, c.SessionID, c.SessionTurn, &rootResult, c.ParentAgentSessionID, c.SnapshotKey); err != nil {
		return 0, err
	}
	if err = NewSessionThreadStore(tx).UpdateTurnComplete(ctx, c.OrgID, c.ThreadID, c.ExpectedTurn, c.Result, c.ProviderSessionID); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `UPDATE code_review_recheck_dispatches SET status='completed',result_message_id=$5,provider_session_id=NULLIF($6,''),snapshot_key=NULLIF($7,''),native_context=$8,completed_at=now(),updated_at=now() WHERE org_id=$1 AND assessment_id=$2 AND job_id=$3 AND attempt_lock_token=$4 AND status='running'`, c.OrgID, c.AssessmentID, c.JobID, c.LockToken, msg.ID, c.ProviderSessionID, c.SnapshotKey, c.NativeContext)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() != 1 {
		return 0, ErrCodeReviewRecheckFence
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return msg.ID, nil
}

// RecordAttemptUsage keeps one observation per job lease and provider launch.
// JSON null means the provider did not return usage; it must not be treated as
// zero cost. Repeated delivery of the same observation is idempotent.
func (s *CodeReviewRecheckStore) RecordAttemptUsage(ctx context.Context, orgID, assessmentID, jobID, lockToken uuid.UUID, attemptKey string, usage json.RawMessage) error {
	if attemptKey == "" || len(attemptKey) > 128 || jobID == uuid.Nil || lockToken == uuid.Nil {
		return fmt.Errorf("invalid code review recheck attempt key")
	}
	if len(usage) == 0 {
		usage = json.RawMessage(`null`)
	}
	if !json.Valid(usage) {
		return fmt.Errorf("invalid attempt usage JSON")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	owned, err := s.LockAttempt(ctx, tx, orgID, assessmentID, jobID, lockToken)
	if err != nil {
		return err
	}
	if !owned {
		return ErrCodeReviewRecheckFence
	}
	var prior json.RawMessage
	err = tx.QueryRow(ctx, `SELECT attempt_usage->$3 FROM code_review_recheck_dispatches WHERE org_id=$1 AND assessment_id=$2`, orgID, assessmentID, attemptKey).Scan(&prior)
	if err != nil {
		return err
	}
	if len(prior) > 0 && string(prior) != "null" {
		if string(prior) != string(usage) {
			return ErrCodeReviewRecheckFence
		}
		return tx.Commit(ctx)
	}
	_, err = tx.Exec(ctx, `UPDATE code_review_recheck_dispatches SET attempt_usage=jsonb_set(attempt_usage,ARRAY[$3]::text[],$4::jsonb,true),updated_at=now() WHERE org_id=$1 AND assessment_id=$2`, orgID, assessmentID, attemptKey, usage)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Fail records a terminal attempt only after ContinueSession has returned and
// unwound its runtime cleanup. It releases thread/session status only when no
// container or thread runtime still owns the workspace. Otherwise the
// supervisor must drain it; a failed timestamp is not proof of process exit.
func (s *CodeReviewRecheckStore) Fail(ctx context.Context, orgID, assessmentID, jobID, lockToken uuid.UUID, detail string) error {
	if detail == "" {
		return fmt.Errorf("recheck failure detail required")
	}
	if len(detail) > 2048 {
		detail = detail[:2048]
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	owned, err := s.LockAttempt(ctx, tx, orgID, assessmentID, jobID, lockToken)
	if err != nil {
		return err
	}
	if !owned {
		return ErrCodeReviewRecheckFence
	}
	var sessionID, threadID uuid.UUID
	var expectedTurn int
	err = tx.QueryRow(ctx, `UPDATE code_review_recheck_dispatches SET status='failed',failure_detail=$5,completed_at=now(),updated_at=now() WHERE org_id=$1 AND assessment_id=$2 AND job_id=$3 AND attempt_lock_token=$4 AND status='running' RETURNING session_id,thread_id,expected_turn`, orgID, assessmentID, jobID, lockToken, detail).Scan(&sessionID, &threadID, &expectedTurn)
	if err != nil {
		return err
	}
	var drained bool
	err = tx.QueryRow(ctx, `SELECT (s.container_id IS NULL AND NOT COALESCE(s.turn_holding_container,false)
		AND NOT EXISTS(SELECT 1 FROM preview_instances p WHERE p.org_id=s.org_id AND p.session_id=s.id AND p.preview_holding_container)
		AND NOT EXISTS(SELECT 1 FROM thread_runtimes r WHERE r.org_id=s.org_id AND r.session_id=s.id AND r.status IN ('starting','live','paused','draining'))
		AND NOT EXISTS(SELECT 1 FROM session_executors e WHERE e.org_id=s.org_id AND e.session_id=s.id AND e.status IN ('starting','running','draining')))
		FROM sessions s WHERE s.org_id=$1 AND s.id=$2 FOR UPDATE`, orgID, sessionID).Scan(&drained)
	if err != nil {
		return err
	}
	if drained {
		if _, err = tx.Exec(ctx, `UPDATE session_threads SET status='idle',current_turn=$4,completed_at=now(),last_activity_at=now() WHERE org_id=$1 AND id=$2 AND session_id=$3 AND current_turn=$4-1 AND status='running'`, orgID, threadID, sessionID, expectedTurn); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE sessions SET status='idle',last_activity_at=now() WHERE org_id=$1 AND id=$2 AND status='running'`, orgID, sessionID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// FailTerminalJob closes a dispatch whose exact bound queue job has already
// become terminal without a completion receipt. A stale worker cannot regain
// authority because Complete requires that same job to be running under its
// current lock token. The old conversation is released only after drain proof.
func (s *CodeReviewRecheckStore) FailTerminalJob(ctx context.Context, orgID, assessmentID uuid.UUID, detail string) (bool, error) {
	if orgID == uuid.Nil || assessmentID == uuid.Nil || detail == "" {
		return false, fmt.Errorf("invalid terminal recheck reconciliation")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var jobStatus string
	var dispatchStatus models.CodeReviewRecheckDispatchStatus
	var sessionID, threadID uuid.UUID
	var expectedTurn int
	err = tx.QueryRow(ctx, `SELECT j.status,d.status,d.session_id,d.thread_id,d.expected_turn
		FROM code_review_recheck_dispatches d JOIN jobs j ON j.org_id=d.org_id AND j.id=d.job_id
		WHERE d.org_id=$1 AND d.assessment_id=$2 FOR UPDATE OF j,d`, orgID, assessmentID).Scan(&jobStatus, &dispatchStatus, &sessionID, &threadID, &expectedTurn)
	if err != nil {
		return false, err
	}
	if jobStatus == "pending" || jobStatus == "running" || dispatchStatus == models.CodeReviewRecheckDispatchCompleted {
		return false, tx.Commit(ctx)
	}
	var cancelled bool
	err = tx.QueryRow(ctx, `SELECT (COALESCE(t.cancel_requested_at >= d.created_at,false) OR EXISTS(
		SELECT 1 FROM session_cancel_requests c WHERE c.org_id=d.org_id AND c.session_id=d.session_id AND c.requested_at >= d.created_at))
		FROM code_review_recheck_dispatches d JOIN session_threads t ON t.org_id=d.org_id AND t.id=d.thread_id
		WHERE d.org_id=$1 AND d.assessment_id=$2`, orgID, assessmentID).Scan(&cancelled)
	if err != nil {
		return false, err
	}
	if cancelled && dispatchStatus != models.CodeReviewRecheckDispatchCancelled {
		_, err = tx.Exec(ctx, `UPDATE code_review_recheck_dispatches SET status='cancelled',failure_detail='user cancelled recheck turn',completed_at=now(),updated_at=now() WHERE org_id=$1 AND assessment_id=$2 AND status IN ('pending','running')`, orgID, assessmentID)
		if err != nil {
			return false, err
		}
		_, err = tx.Exec(ctx, `UPDATE code_review_revision_assessments SET status='cancelled',failure_detail='user cancelled recheck turn',completed_at=now(),updated_at=now() WHERE org_id=$1 AND id=$2 AND status='running'`, orgID, assessmentID)
		if err != nil {
			return false, err
		}
	} else if dispatchStatus != models.CodeReviewRecheckDispatchFailed && dispatchStatus != models.CodeReviewRecheckDispatchCancelled {
		_, err = tx.Exec(ctx, `UPDATE code_review_recheck_dispatches SET status='failed',failure_detail=$3,completed_at=now(),updated_at=now() WHERE org_id=$1 AND assessment_id=$2 AND status IN ('pending','running')`, orgID, assessmentID, detail)
		if err != nil {
			return false, err
		}
	}
	var drained bool
	err = tx.QueryRow(ctx, `SELECT (s.container_id IS NULL AND NOT COALESCE(s.turn_holding_container,false)
		AND NOT EXISTS(SELECT 1 FROM preview_instances p WHERE p.org_id=s.org_id AND p.session_id=s.id AND p.preview_holding_container)
		AND NOT EXISTS(SELECT 1 FROM thread_runtimes r WHERE r.org_id=s.org_id AND r.session_id=s.id AND r.status IN ('starting','live','paused','draining'))
		AND NOT EXISTS(SELECT 1 FROM session_executors e WHERE e.org_id=s.org_id AND e.session_id=s.id AND e.status IN ('starting','running','draining')))
		FROM sessions s WHERE s.org_id=$1 AND s.id=$2 FOR UPDATE`, orgID, sessionID).Scan(&drained)
	if err != nil {
		return false, err
	}
	if drained {
		if _, err = tx.Exec(ctx, `UPDATE session_threads SET status='idle',current_turn=$4,completed_at=now(),last_activity_at=now() WHERE org_id=$1 AND id=$2 AND session_id=$3 AND current_turn=$4-1 AND status='running'`, orgID, threadID, sessionID, expectedTurn); err != nil {
			return false, err
		}
		if _, err = tx.Exec(ctx, `UPDATE sessions SET status='idle',last_activity_at=now() WHERE org_id=$1 AND id=$2 AND status='running'`, orgID, sessionID); err != nil {
			return false, err
		}
	}
	return true, tx.Commit(ctx)
}

// Cancel records a user-cancelled exact turn and assessment under the active
// job lease. Cancellation is terminal without scheduling a forced full review.
func (s *CodeReviewRecheckStore) Cancel(ctx context.Context, orgID, assessmentID, jobID, lockToken uuid.UUID) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	owned, err := s.LockAttempt(ctx, tx, orgID, assessmentID, jobID, lockToken)
	if err != nil {
		return err
	}
	if !owned {
		return ErrCodeReviewRecheckFence
	}
	var sessionID, threadID uuid.UUID
	var expectedTurn int
	err = tx.QueryRow(ctx, `UPDATE code_review_recheck_dispatches SET status='cancelled',failure_detail='user cancelled recheck turn',completed_at=now(),updated_at=now() WHERE org_id=$1 AND assessment_id=$2 AND job_id=$3 AND attempt_lock_token=$4 AND status='running' RETURNING session_id,thread_id,expected_turn`, orgID, assessmentID, jobID, lockToken).Scan(&sessionID, &threadID, &expectedTurn)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE code_review_revision_assessments SET status='cancelled',failure_detail='user cancelled recheck turn',completed_at=now(),updated_at=now() WHERE org_id=$1 AND id=$2 AND status='running' AND review_scope='evidence_only'`, orgID, assessmentID)
	if err != nil {
		return err
	}
	var drained bool
	err = tx.QueryRow(ctx, `SELECT (s.container_id IS NULL AND NOT COALESCE(s.turn_holding_container,false)
		AND NOT EXISTS(SELECT 1 FROM preview_instances p WHERE p.org_id=s.org_id AND p.session_id=s.id AND p.preview_holding_container)
		AND NOT EXISTS(SELECT 1 FROM thread_runtimes r WHERE r.org_id=s.org_id AND r.session_id=s.id AND r.status IN ('starting','live','paused','draining'))
		AND NOT EXISTS(SELECT 1 FROM session_executors e WHERE e.org_id=s.org_id AND e.session_id=s.id AND e.status IN ('starting','running','draining')))
		FROM sessions s WHERE s.org_id=$1 AND s.id=$2 FOR UPDATE`, orgID, sessionID).Scan(&drained)
	if err != nil {
		return err
	}
	if drained {
		if _, err = tx.Exec(ctx, `UPDATE session_threads SET status='idle',current_turn=$4,completed_at=now(),last_activity_at=now() WHERE org_id=$1 AND id=$2 AND session_id=$3 AND current_turn=$4-1 AND status IN ('running','cancelled')`, orgID, threadID, sessionID, expectedTurn); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE sessions SET status='idle',last_activity_at=now() WHERE org_id=$1 AND id=$2 AND status IN ('running','cancelled')`, orgID, sessionID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ReconcileDrainedTerminalTurn releases a previously terminal turn after its
// sandbox and executor have visibly stopped. A sweep can call this repeatedly;
// it never releases a live or nonterminal dispatch.
func (s *CodeReviewRecheckStore) ReconcileDrainedTerminalTurn(ctx context.Context, orgID, assessmentID uuid.UUID) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var sessionID, threadID, pullRequestID uuid.UUID
	var expectedTurn int
	err = tx.QueryRow(ctx, `SELECT session_id,thread_id,pull_request_id,expected_turn FROM code_review_recheck_dispatches WHERE org_id=$1 AND assessment_id=$2 AND status IN ('failed','cancelled') FOR UPDATE`, orgID, assessmentID).Scan(&sessionID, &threadID, &pullRequestID, &expectedTurn)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var drained bool
	err = tx.QueryRow(ctx, `SELECT (s.container_id IS NULL AND NOT COALESCE(s.turn_holding_container,false)
		AND NOT EXISTS(SELECT 1 FROM preview_instances p WHERE p.org_id=s.org_id AND p.session_id=s.id AND p.preview_holding_container)
		AND NOT EXISTS(SELECT 1 FROM thread_runtimes r WHERE r.org_id=s.org_id AND r.session_id=s.id AND r.status IN ('starting','live','paused','draining'))
		AND NOT EXISTS(SELECT 1 FROM session_executors e WHERE e.org_id=s.org_id AND e.session_id=s.id AND e.status IN ('starting','running','draining')))
		FROM sessions s WHERE s.org_id=$1 AND s.id=$2 AND s.code_review_owner_pr_id=$3 FOR UPDATE`, orgID, sessionID, pullRequestID).Scan(&drained)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil || !drained {
		return false, err
	}
	threadTag, err := tx.Exec(ctx, `UPDATE session_threads SET status='idle',current_turn=$4,completed_at=now(),last_activity_at=now() WHERE org_id=$1 AND id=$2 AND session_id=$3 AND current_turn=$4-1 AND status IN ('running','cancelled')`, orgID, threadID, sessionID, expectedTurn)
	if err != nil {
		return false, err
	}
	if threadTag.RowsAffected() == 0 {
		var currentTurn int
		err = tx.QueryRow(ctx, `SELECT current_turn FROM session_threads WHERE org_id=$1 AND id=$2 AND session_id=$3 AND status='idle'`, orgID, threadID, sessionID).Scan(&currentTurn)
		if err != nil || currentTurn != expectedTurn {
			return false, err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE sessions SET status='idle',last_activity_at=now() WHERE org_id=$1 AND id=$2 AND code_review_owner_pr_id=$3 AND status IN ('running','cancelled')`, orgID, sessionID, pullRequestID)
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
