package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

var ErrCodeReviewRequestConflict = errors.New("review request identity already belongs to different input")

// CodeReviewScheduleStore owns PR-level serialization, independent of session
// creation. All scheduler writers use WithLockedPR, including wake completion.
type CodeReviewScheduleStore struct {
	db      TxStarter
	streams *cache.CodeReviewStreams
	logger  zerolog.Logger
}

func NewCodeReviewScheduleStore(conn TxStarter) *CodeReviewScheduleStore {
	return &CodeReviewScheduleStore{db: conn}
}

func (s *CodeReviewScheduleStore) Get(ctx context.Context, orgID, prID uuid.UUID) (models.CodeReviewPRState, error) {
	rows, err := s.db.Query(ctx, `SELECT * FROM code_review_pr_state WHERE org_id=$1 AND pull_request_id=$2`, orgID, prID)
	if err != nil {
		return models.CodeReviewPRState{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CodeReviewPRState])
}

func (s *CodeReviewScheduleStore) WithLockedPR(ctx context.Context, orgID, repoID, prID uuid.UUID, fn func(pgx.Tx, *models.CodeReviewPRState) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "code_review_pr:"+orgID.String()+":"+prID.String()); err != nil {
		return err
	}
	// Validate tenant and repository ownership even when a scheduling row exists.
	var owned bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pull_requests p JOIN repositories r ON r.org_id=p.org_id AND r.full_name=p.github_repo WHERE p.org_id=$1 AND p.id=$2 AND r.id=$3)`, orgID, prID, repoID).Scan(&owned); err != nil {
		return err
	}
	if !owned {
		return pgx.ErrNoRows
	}
	if _, err = tx.Exec(ctx, `INSERT INTO code_review_pr_state(org_id,repository_id,pull_request_id) VALUES($1,$2,$3) ON CONFLICT(org_id,pull_request_id) DO NOTHING`, orgID, repoID, prID); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT * FROM code_review_pr_state WHERE org_id=$1 AND pull_request_id=$2 FOR UPDATE`, orgID, prID)
	if err != nil {
		return err
	}
	state, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CodeReviewPRState])
	if err != nil {
		return err
	}
	if err = fn(tx, &state); err != nil {
		return err
	}
	if err = state.State.Validate(); err != nil {
		return err
	}
	if err = state.WaitReason.Validate(); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE code_review_pr_state SET generation=$3,automatic_paused=$4,head_sha=$5,base_sha=$6,base_ref=$7,is_draft=$8,snapshot_observed_at=$9,last_material_change_at=$10,first_pending_at=$11,last_agent_start_at=$12,eligible_at=$13,retry_at=$14,active_session_id=$15,pending_request_id=$16,pending_input=$17,state=$18,wait_reason=$19,updated_at=now() WHERE org_id=$1 AND pull_request_id=$2`, orgID, prID, state.Generation, state.AutomaticPaused, state.HeadSHA, state.BaseSHA, state.BaseRef, state.IsDraft, state.SnapshotObservedAt, state.LastMaterialChangeAt, state.FirstPendingAt, state.LastAgentStartAt, state.EligibleAt, state.RetryAt, state.ActiveSessionID, state.PendingRequestID, state.PendingInput, state.State, state.WaitReason)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if s.streams != nil {
		if err := s.streams.PublishUpdated(ctx, orgID, models.CodeReviewUpdatedEvent{OrgID: orgID, PullRequestID: &prID, SchedulingState: state.State, UpdatedAt: time.Now().UTC()}); err != nil {
			s.logger.Warn().Err(err).Str("pull_request_id", prID.String()).Msg("publish scheduling update failed")
		}
	}
	return nil
}

// UpsertCodeReviewWake is called under the PR lock. Running jobs are left to
// reread state and reschedule themselves with their lease under the same lock.
func UpsertCodeReviewWake(ctx context.Context, tx pgx.Tx, orgID, prID uuid.UUID, at time.Time) error {
	key := "code_review_schedule:" + prID.String()
	tag, err := tx.Exec(ctx, `UPDATE jobs SET run_at=$3,updated_at=now() WHERE org_id=$1 AND queue='agent' AND dedupe_key=$2 AND status='pending'`, orgID, key, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	_, err = NewJobStore(tx).EnqueueWithOpts(ctx, orgID, EnqueueOpts{Queue: "agent", JobType: models.JobTypeReconcileCodeReviewSchedule, Payload: models.CodeReviewScheduleWake{OrgID: orgID, PullRequestID: prID}, DedupeKey: &key, RunAt: &at, Priority: 5, MaxAttempts: 8})
	return err
}

// RecordCodeReviewRequest preserves delivery identity separately from revision
// generation. It returns the existing request on a matching redelivery.
func RecordCodeReviewRequest(ctx context.Context, tx pgx.Tx, orgID, repoID, prID uuid.UUID, kind, identity string, mode models.CodeReviewRequestMode, hash string, generation int64, requesterID *uuid.UUID) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `INSERT INTO code_review_requests(org_id,repository_id,pull_request_id,source_kind,source_identity,mode,input_hash,target_generation,requester_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(org_id,source_kind,source_identity) DO NOTHING RETURNING id`, orgID, repoID, prID, kind, identity, mode, hash, generation, requesterID).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return id, false, err
	}
	var existingHash string
	var existingPR uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id,input_hash,pull_request_id FROM code_review_requests WHERE org_id=$1 AND source_kind=$2 AND source_identity=$3`, orgID, kind, identity).Scan(&id, &existingHash, &existingPR)
	if err != nil {
		return id, false, err
	}
	if hash != existingHash || prID != existingPR {
		return id, false, ErrCodeReviewRequestConflict
	}
	return id, true, nil
}

func ResolveCodeReviewRequests(ctx context.Context, tx pgx.Tx, orgID, prID uuid.UUID, generation int64, sessionID *uuid.UUID, status string) error {
	_, err := tx.Exec(ctx, `UPDATE code_review_requests SET session_id=$4,status=$5 WHERE org_id=$1 AND pull_request_id=$2 AND target_generation<=$3 AND status IN ('pending','joined')`, orgID, prID, generation, sessionID, status)
	return err
}

func EncodeCodeReviewScheduleInput(input any) (json.RawMessage, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode pending review: %w", err)
	}
	return raw, nil
}

// ListPending exposes durable requests that have no session yet. Keeping this
// queue separate preserves the existing review history's filter semantics.
func (s *CodeReviewScheduleStore) ListPending(ctx context.Context, orgID uuid.UUID, repoID *uuid.UUID, after *uuid.UUID, limit int) ([]models.CodeReviewScheduledTarget, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `SELECT row_to_json(st) AS schedule,p.title,p.github_pr_url,p.github_pr_number,p.github_repo FROM code_review_pr_state st JOIN pull_requests p ON p.org_id=st.org_id AND p.id=st.pull_request_id WHERE st.org_id=$1 AND st.pending_input IS NOT NULL AND ($2::uuid IS NULL OR st.repository_id=$2) AND ($3::uuid IS NULL OR st.id>$3) ORDER BY st.id LIMIT $4`, orgID, repoID, after, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.CodeReviewScheduledTarget])
}

// SetStreams configures post-commit scheduling notifications.
// lint:allow-no-orgid reason="process-wide dependency injection for review scheduling events"
func (s *CodeReviewScheduleStore) SetStreams(streams *cache.CodeReviewStreams, logger zerolog.Logger) {
	s.streams = streams
	s.logger = logger
}

// RepairMissingWakes restores bounded pending work after dead-lettered/lost
// starter jobs. It changes no PR state; each wake rechecks ownership and state.
// lint:allow-no-orgid reason="system scheduler repairs missing wake jobs across tenants in bounded batches"
func (s *CodeReviewScheduleStore) RepairMissingWakes(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `INSERT INTO jobs(org_id,queue,job_type,payload,priority,dedupe_key,run_at,max_attempts)
 SELECT st.org_id,'agent','reconcile_code_review_schedule',jsonb_build_object('org_id',st.org_id,'pull_request_id',st.pull_request_id),5,'code_review_schedule:'||st.pull_request_id::text,GREATEST(now(),COALESCE(st.retry_at,st.eligible_at,now())),8
 FROM code_review_pr_state st WHERE (st.pending_input IS NOT NULL OR EXISTS(SELECT 1 FROM code_review_session_metadata m JOIN session_threads t ON t.org_id=m.org_id AND t.session_id=m.session_id WHERE m.org_id=st.org_id AND m.pull_request_id=st.pull_request_id AND m.status='stale' AND t.status IN ('pending','running','awaiting_input'))) AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.org_id=st.org_id AND j.queue='agent' AND j.dedupe_key='code_review_schedule:'||st.pull_request_id::text AND j.status IN ('pending','running'))
 ORDER BY st.first_pending_at,st.id LIMIT 100 ON CONFLICT DO NOTHING`)
	return err
}

// StaleActiveSessions makes post-commit cancellation recoverable even when the
// process died after marking metadata stale or while sending a cancellation.
func (s *CodeReviewScheduleStore) StaleActiveSessions(ctx context.Context, orgID, prID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.db.Query(ctx, `SELECT m.session_id FROM code_review_session_metadata m WHERE m.org_id=$1 AND m.pull_request_id=$2 AND m.status='stale' AND EXISTS(SELECT 1 FROM session_threads t WHERE t.org_id=m.org_id AND t.session_id=m.session_id AND t.status IN ('pending','running','awaiting_input'))`, orgID, prID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}

// HasActiveCodeReview is called inside the PR admission transaction. A
// stranded metadata row with no starter job or threads must not block forever;
// startReview retains its existing failed-attempt recovery for that case.
func HasActiveCodeReview(ctx context.Context, tx pgx.Tx, orgID, prID uuid.UUID, enqueueGrace time.Duration) (bool, error) {
	var active bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM code_review_session_metadata m WHERE m.org_id=$1 AND m.pull_request_id=$2 AND (
 EXISTS(SELECT 1 FROM session_threads t WHERE t.org_id=m.org_id AND t.session_id=m.session_id AND t.status IN ('pending','running','awaiting_input')) OR
 (m.status IN ('queued','running') AND (m.created_at > now()-$3::interval OR EXISTS(SELECT 1 FROM jobs j WHERE j.org_id=m.org_id AND j.queue='agent' AND j.dedupe_key='code_review:'||m.review_output_key AND j.status IN ('pending','running'))))))`, orgID, prID, enqueueGrace.String()).Scan(&active)
	return active, err
}
