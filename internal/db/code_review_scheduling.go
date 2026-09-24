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

type CodeReviewRequestRecord struct {
	ID           uuid.UUID
	Mode         models.CodeReviewRequestMode
	InputHash    string
	Status       string
	SessionID    *uuid.UUID
	AssessmentID *uuid.UUID
	RequesterID  *uuid.UUID
}

func (s *CodeReviewScheduleStore) GetRequestByIdentity(ctx context.Context, orgID uuid.UUID, kind, identity string) (CodeReviewRequestRecord, error) {
	var r CodeReviewRequestRecord
	err := s.db.QueryRow(ctx, `SELECT id,mode,input_hash,status,session_id,assessment_id,requester_id FROM code_review_requests WHERE org_id=$1 AND source_kind=$2 AND source_identity=$3`, orgID, kind, identity).Scan(&r.ID, &r.Mode, &r.InputHash, &r.Status, &r.SessionID, &r.AssessmentID, &r.RequesterID)
	return r, err
}

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

// GetRepositoryIDForPR resolves an existing tenant PR before its first review
// creates either schedule state or legacy session metadata.
func (s *CodeReviewScheduleStore) GetRepositoryIDForPR(ctx context.Context, orgID, prID uuid.UUID) (uuid.UUID, error) {
	var repositoryID uuid.UUID
	err := s.db.QueryRow(ctx, `SELECT r.id FROM pull_requests p JOIN repositories r ON r.org_id=p.org_id AND r.full_name=p.github_repo WHERE p.org_id=$1 AND p.id=$2`, orgID, prID).Scan(&repositoryID)
	return repositoryID, err
}

// GetLatestAssessment and GetLatestFullBaseline are read-only hints before
// network capture. Admission rereads them under WithLockedPR before writing.
func (s *CodeReviewScheduleStore) GetLatestAssessment(ctx context.Context, orgID, prID uuid.UUID) (models.CodeReviewAssessment, error) {
	rows, err := s.db.Query(ctx, `SELECT * FROM code_review_revision_assessments WHERE org_id=$1 AND pull_request_id=$2 ORDER BY generation DESC LIMIT 1`, orgID, prID)
	if err != nil {
		return models.CodeReviewAssessment{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CodeReviewAssessment])
}

func (s *CodeReviewScheduleStore) GetLatestFullBaseline(ctx context.Context, orgID, prID uuid.UUID) (models.CodeReviewAssessment, error) {
	rows, err := s.db.Query(ctx, `SELECT * FROM code_review_revision_assessments WHERE org_id=$1 AND pull_request_id=$2 AND review_scope='full' AND status='completed' ORDER BY generation DESC LIMIT 1`, orgID, prID)
	if err != nil {
		return models.CodeReviewAssessment{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CodeReviewAssessment])
}

func (s *CodeReviewScheduleStore) GetAssessmentByID(ctx context.Context, orgID, assessmentID uuid.UUID) (models.CodeReviewAssessment, error) {
	rows, err := s.db.Query(ctx, `SELECT * FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, orgID, assessmentID)
	if err != nil {
		return models.CodeReviewAssessment{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.CodeReviewAssessment])
}

// MarkFallbackQueued records that the forced full request was durably admitted.
// Repeated supervisors can safely call this after observing the same request.
func (s *CodeReviewScheduleStore) MarkFallbackQueued(ctx context.Context, orgID, assessmentID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `UPDATE code_review_revision_assessments SET failure_detail=regexp_replace(failure_detail,'^full_review:','full_review_queued:') WHERE org_id=$1 AND id=$2 AND status IN ('failed','superseded') AND failure_detail LIKE 'full_review:%'`, orgID, assessmentID)
	if err != nil {
		return err
	}
	return nil
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
	_, err = tx.Exec(ctx, `UPDATE code_review_pr_state SET generation=$3,automatic_paused=$4,head_sha=$5,base_sha=$6,base_ref=$7,is_draft=$8,snapshot_observed_at=$9,last_material_change_at=$10,first_pending_at=$11,last_agent_start_at=$12,eligible_at=$13,retry_at=$14,active_session_id=$15,pending_request_id=$16,pending_input=$17,state=$18,wait_reason=$19,active_assessment_id=$20,current_assessment_id=$21,updated_at=now() WHERE org_id=$1 AND pull_request_id=$2`, orgID, prID, state.Generation, state.AutomaticPaused, state.HeadSHA, state.BaseSHA, state.BaseRef, state.IsDraft, state.SnapshotObservedAt, state.LastMaterialChangeAt, state.FirstPendingAt, state.LastAgentStartAt, state.EligibleAt, state.RetryAt, state.ActiveSessionID, state.PendingRequestID, state.PendingInput, state.State, state.WaitReason, state.ActiveAssessmentID, state.CurrentAssessmentID)
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
 FROM code_review_pr_state st WHERE (st.pending_input IS NOT NULL OR EXISTS(
 SELECT 1 FROM code_review_revision_assessments a JOIN code_review_session_metadata m ON m.org_id=a.org_id AND m.id=a.metadata_id
 WHERE a.org_id=st.org_id AND a.pull_request_id=st.pull_request_id AND a.review_scope='full'
 AND m.status IN ('stale','failed','cancelled') AND a.publication_receipt IS NULL AND a.github_review_id IS NULL
 AND ((a.status IN ('reserved','running') AND a.publication_state='not_started') OR (a.status='publishing' AND a.publication_state='reserved'))
 ) OR EXISTS(SELECT 1 FROM code_review_session_metadata m JOIN session_threads t ON t.org_id=m.org_id AND t.session_id=m.session_id WHERE m.org_id=st.org_id AND m.pull_request_id=st.pull_request_id AND m.status='stale' AND t.status IN ('pending','running','awaiting_input'))) AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.org_id=st.org_id AND j.queue='agent' AND j.dedupe_key='code_review_schedule:'||st.pull_request_id::text AND j.status IN ('pending','running'))
 ORDER BY st.first_pending_at,st.id LIMIT 100 ON CONFLICT DO NOTHING`)
	if err != nil {
		return err
	}
	// Stop automatic reconciliation after a bounded window without releasing
	// the reservation for an external send whose outcome is still unknown.
	_, err = s.db.Exec(ctx, `UPDATE code_review_revision_assessments a SET failure_detail=$1
	 FROM (SELECT org_id,id FROM code_review_revision_assessments
	 WHERE status='publishing' AND publication_state='uncertain' AND result_origin IS NOT NULL
	 AND created_at <= now()-make_interval(secs => $2)
	 AND COALESCE(failure_detail,'') NOT LIKE 'operator_reconciliation_required:%'
	 ORDER BY created_at,id LIMIT 100) expired
	 WHERE a.org_id=expired.org_id AND a.id=expired.id
	 AND a.status='publishing' AND a.publication_state='uncertain'`, CodeReviewPublicationOperatorRequired, CodeReviewPublicationReconciliationWindow.Seconds())
	if err != nil {
		return err
	}
	// A lost or exhausted supervisor must not strand a reserved assessment.
	// Retrying a superseded row also closes a crash between retirement and
	// admission of its replacement, preserving the original request identity.
	_, err = s.db.Exec(ctx, `INSERT INTO jobs(org_id,queue,job_type,payload,priority,dedupe_key,run_at,max_attempts)
 SELECT a.org_id,'agent','run_code_review_recheck',jsonb_build_object('org_id',a.org_id,'assessment_id',a.id),5,'code_review_recheck:'||a.id::text,now(),8
 FROM code_review_revision_assessments a
 WHERE ((a.review_scope='evidence_only' AND a.status IN ('reserved','running','publishing')) OR (a.status IN ('failed','superseded') AND (a.failure_detail LIKE 'full_review:%' OR a.failure_detail LIKE 'evidence_recheck:%')))
 AND COALESCE(a.failure_detail,'') NOT LIKE 'operator_reconciliation_required:%'
 AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.org_id=a.org_id AND j.queue='agent' AND j.dedupe_key='code_review_recheck:'||a.id::text AND j.status IN ('pending','running'))
 ORDER BY a.created_at,a.id LIMIT 100 ON CONFLICT DO NOTHING`)
	if err != nil {
		return err
	}
	// Full-review publication uses its original controller payload. Reuse all
	// captured provenance, including request/dispute identity and fork context;
	// never construct a new review or infer that an uncertain send was absent.
	_, err = s.db.Exec(ctx, `INSERT INTO jobs(org_id,queue,job_type,payload,priority,dedupe_key,run_at,max_attempts)
 SELECT a.org_id,'agent','run_code_review',original.payload,5,original.dedupe_key,now(),8
 FROM code_review_revision_assessments a
 JOIN LATERAL (SELECT j.payload,j.dedupe_key FROM jobs j
   WHERE j.org_id=a.org_id AND j.queue='agent' AND j.job_type='run_code_review'
   AND j.payload->>'session_id'=a.session_id::text AND j.payload->>'review_output_key'=a.publication_key
   AND j.dedupe_key='code_review:'||a.publication_key
   ORDER BY j.created_at DESC,j.id DESC LIMIT 1) original ON true
 WHERE a.review_scope='full' AND a.status IN ('running','publishing') AND a.result_origin IS NOT NULL
 AND COALESCE(a.failure_detail,'') NOT LIKE 'operator_reconciliation_required:%'
 AND NOT EXISTS(SELECT 1 FROM jobs active WHERE active.org_id=a.org_id AND active.queue='agent'
   AND active.dedupe_key=original.dedupe_key AND active.status IN ('pending','running'))
 ORDER BY a.created_at,a.id LIMIT 100 ON CONFLICT DO NOTHING`)
	if err != nil {
		return err
	}
	// A bound continuation job may become terminal after its supervisor has
	// already failed the assessment (for example at the publication deadline).
	// Close that exact dispatch, then release its old conversation only after
	// runtime drain is proven. Keep this scan bounded and outside the PR lock.
	rows, err := s.db.Query(ctx, `SELECT d.org_id,d.assessment_id,d.status
	 FROM code_review_recheck_dispatches d
	 JOIN jobs j ON j.org_id=d.org_id AND j.id=d.job_id
	 JOIN sessions sess ON sess.org_id=d.org_id AND sess.id=d.session_id
	 JOIN session_threads t ON t.org_id=d.org_id AND t.id=d.thread_id
	 WHERE (d.status IN ('pending','running') AND j.status IN ('succeeded','failed','cancelled','dead_letter'))
	    OR (d.status IN ('failed','cancelled') AND (sess.status IN ('running','cancelled') OR t.status IN ('running','cancelled')))
	 ORDER BY d.created_at,d.assessment_id LIMIT 100`)
	if err != nil {
		return err
	}
	type terminalTurnCandidate struct {
		orgID, assessmentID uuid.UUID
		status              string
	}
	var terminalTurns []terminalTurnCandidate
	for rows.Next() {
		var candidate terminalTurnCandidate
		if err := rows.Scan(&candidate.orgID, &candidate.assessmentID, &candidate.status); err != nil {
			rows.Close()
			return err
		}
		terminalTurns = append(terminalTurns, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	rechecks := NewCodeReviewRecheckStore(s.db)
	for _, candidate := range terminalTurns {
		if candidate.status == "pending" || candidate.status == "running" {
			if _, err := rechecks.FailTerminalJob(ctx, candidate.orgID, candidate.assessmentID, "bound continuation job ended before a validated result"); err != nil {
				return err
			}
		}
		if _, err := rechecks.ReconcileDrainedTerminalTurn(ctx, candidate.orgID, candidate.assessmentID); err != nil {
			return err
		}
	}
	// Failed assessments can reach a terminal state through job exhaustion or a
	// kill switch, outside the normal publication transaction. Reconcile their
	// scheduler pointer before the next admission or pending wake.
	rows, err = s.db.Query(ctx, `SELECT a.org_id,a.id FROM code_review_revision_assessments a
	 JOIN code_review_pr_state st ON st.org_id=a.org_id AND st.pull_request_id=a.pull_request_id
	 WHERE st.active_assessment_id=a.id AND a.status IN ('failed','cancelled','superseded')
	 ORDER BY a.created_at,a.id LIMIT 100`)
	if err != nil {
		return err
	}
	type terminalAssessment struct{ orgID, assessmentID uuid.UUID }
	var terminal []terminalAssessment
	for rows.Next() {
		var candidate terminalAssessment
		if err := rows.Scan(&candidate.orgID, &candidate.assessmentID); err != nil {
			rows.Close()
			return err
		}
		terminal = append(terminal, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, candidate := range terminal {
		if err := s.SettleAssessment(ctx, candidate.orgID, candidate.assessmentID); err != nil {
			return err
		}
	}
	// A completed replacement may outlive the old session's runtime. Retire its
	// ownership only after the runtime drains, using the same guarded PR lock as
	// the immediate completion path.
	rows, err = s.db.Query(ctx, `SELECT s.org_id,s.id,s.code_review_owner_pr_id,replacement.id
	 FROM sessions s
	 JOIN LATERAL (SELECT a.id,a.generation FROM code_review_revision_assessments a
	   WHERE a.org_id=s.org_id AND a.pull_request_id=s.code_review_owner_pr_id
	     AND a.review_scope='full' AND a.status='completed' AND a.session_id<>s.id
	   ORDER BY a.generation DESC LIMIT 1) replacement ON true
	 WHERE s.code_review_owner_pr_id IS NOT NULL AND s.status IN ('idle','completed')
	   AND s.container_id IS NULL AND NOT COALESCE(s.turn_holding_container,false)
	   AND replacement.generation > COALESCE((SELECT MAX(prior.generation) FROM code_review_revision_assessments prior
	     WHERE prior.org_id=s.org_id AND prior.pull_request_id=s.code_review_owner_pr_id AND prior.session_id=s.id),0)
	   AND NOT EXISTS (SELECT 1 FROM code_review_recheck_dispatches d WHERE d.org_id=s.org_id AND d.session_id=s.id AND d.status IN ('pending','running'))
	   AND NOT EXISTS (SELECT 1 FROM thread_runtimes r WHERE r.org_id=s.org_id AND r.session_id=s.id AND r.status IN ('starting','live','paused','draining'))
	   AND NOT EXISTS (SELECT 1 FROM session_executors e WHERE e.org_id=s.org_id AND e.session_id=s.id AND e.status IN ('starting','running','draining'))
	   AND NOT EXISTS (SELECT 1 FROM session_threads t WHERE t.org_id=s.org_id AND t.session_id=s.id AND t.status NOT IN ('idle','completed','cancelled','failed'))
	 ORDER BY s.id LIMIT 100`)
	if err != nil {
		return err
	}
	type retiredCandidate struct{ orgID, sessionID, pullRequestID, replacementID uuid.UUID }
	var retired []retiredCandidate
	for rows.Next() {
		var candidate retiredCandidate
		if err := rows.Scan(&candidate.orgID, &candidate.sessionID, &candidate.pullRequestID, &candidate.replacementID); err != nil {
			rows.Close()
			return err
		}
		retired = append(retired, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, candidate := range retired {
		tx, err := s.db.Begin(ctx)
		if err != nil {
			return err
		}
		_, err = NewCodeReviewRecheckStore(tx).RetireOwnerForCompletedReplacement(ctx, tx, candidate.orgID, candidate.sessionID, candidate.pullRequestID, candidate.replacementID)
		if err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
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
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM code_review_revision_assessments a WHERE a.org_id=$1 AND a.pull_request_id=$2 AND a.status IN ('reserved','running','publishing')) OR EXISTS(SELECT 1 FROM code_review_session_metadata m WHERE m.org_id=$1 AND m.pull_request_id=$2 AND (
 EXISTS(SELECT 1 FROM session_threads t WHERE t.org_id=m.org_id AND t.session_id=m.session_id AND t.status IN ('pending','running','awaiting_input')) OR
 (m.status IN ('queued','running') AND (m.created_at > now()-$3::interval OR EXISTS(SELECT 1 FROM jobs j WHERE j.org_id=m.org_id AND j.queue='agent' AND j.dedupe_key='code_review:'||m.review_output_key AND j.status IN ('pending','running'))))))`, orgID, prID, enqueueGrace.String()).Scan(&active)
	return active, err
}
