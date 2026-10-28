package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// EndedFullReviewParams pins recovery to the original controller and parent turn.
type EndedFullReviewParams struct {
	RepositoryID, PullRequestID, SessionID, MetadataID, PolicyID uuid.UUID
	JobID, JobLockToken                                          uuid.UUID
	HeadSHA, OutputKey                                           string
	SessionStatus                                                models.SessionStatus
	TriggeringDisputeID                                          *uuid.UUID
	SessionTurn                                                  int
}

// ReconcileEndedFullReview either permits consumption of validated saved work,
// or atomically fails an unsent review whose parent ended without that work.
// It never starts a new agent turn or modifies saved reviewer evidence. An idle
// workspace reference may remain until this controller settles and GC can run.
// The explicit existing Run owns the complete DB-only unit; the receiver's DBTX
// does not imply ownership. canResume must be a pure predicate over saved work.
func (s *CodeReviewScheduleStore) ReconcileEndedFullReview(ctx context.Context, orgID uuid.UUID, transactions CodeReviewMetadataTransactions, in EndedFullReviewParams, canResume func([]models.CodeReviewAgentResult) bool) (bool, error) {
	if s == nil || ctx == nil || nilLifecycleDependency(transactions) {
		return false, ErrCodeReviewMetadataTerminalCapability
	}
	resumable := false
	called := false
	var staged models.CodeReviewPRState
	err := transactions.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		// A replay must not retain a prior provisional resume or event result.
		resumable, called = false, false
		staged = models.CodeReviewPRState{}
		state, err := s.withLockedPRInTx(unitCtx, tx, orgID, in.RepositoryID, in.PullRequestID, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
			if in.SessionStatus != models.SessionStatusCompleted && in.SessionStatus != models.SessionStatusFailed {
				return ErrCodeReviewAssessmentState
			}
			// Lock every actual metadata row for this exact session before the job,
			// parent session, assessment or results. The existing PR domain is held
			// first; a metadata SHARE/session-FK installer cannot invert that order.
			locked, err := lockCodeReviewMetadataTerminal(unitCtx, tx, codeReviewMetadataTerminalSessionLocks, pgx.NamedArgs{
				"org_id": orgID, "session_id": in.SessionID, "active_only": false,
			})
			if err != nil {
				return err
			}
			if len(locked) == 0 {
				return pgx.ErrNoRows
			}
			reviews, err := NewCodeReviewStoreInTx(tx)
			if err != nil {
				return err
			}
			if err := reviews.LockAssessmentPublicationJob(unitCtx, orgID, in.JobID, in.JobLockToken); err != nil {
				return err
			}
			var reason string
			err = tx.QueryRow(unitCtx, `SELECT COALESCE(s.failure_explanation,'') FROM sessions s
   WHERE s.org_id=$1 AND s.id=$2 AND s.origin='code_review' AND s.repository_id=$3
   AND s.status=$4 AND s.current_turn=$5
   AND (s.code_review_owner_pr_id IS NULL OR s.code_review_owner_pr_id=$6)
   AND (`+codeReviewSessionExecutionDrainedSQL+`)
   AND NOT EXISTS(SELECT 1 FROM session_threads t WHERE t.org_id=s.org_id AND t.session_id=s.id AND t.status IN ('pending','running','awaiting_input'))
   AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.org_id=s.org_id AND j.status IN ('pending','running')
    AND j.payload->>'session_id'=s.id::text AND j.id<>$7
    AND j.job_type IN ('run_code_review','run_agent','continue_session','fork_session_thread','revert_session_thread','deliver_thread_inbox','prepare_code_review_workspace'))
   AND NOT EXISTS(SELECT 1 FROM code_review_recheck_dispatches d WHERE d.org_id=s.org_id AND d.session_id=s.id AND d.status IN ('pending','running'))
   FOR UPDATE OF s`, orgID, in.SessionID, in.RepositoryID, in.SessionStatus, in.SessionTurn, in.PullRequestID, in.JobID).Scan(&reason)
			if err != nil {
				return fmt.Errorf("ended review parent is not exclusively drained: %w", err)
			}
			var assessmentID uuid.UUID
			err = tx.QueryRow(unitCtx, `SELECT a.id FROM code_review_revision_assessments a
   JOIN code_review_session_metadata m ON m.org_id=a.org_id AND m.id=a.metadata_id AND m.session_id=a.session_id
   WHERE a.org_id=$1 AND a.session_id=$2 AND a.metadata_id=$3 AND a.repository_id=$4 AND a.pull_request_id=$5
   AND a.policy_id=$6 AND a.head_sha=$7 AND a.publication_key=$8
   AND m.repository_id=a.repository_id AND m.pull_request_id=a.pull_request_id AND m.policy_id=a.policy_id
   AND m.head_sha=a.head_sha AND m.review_output_key=a.publication_key AND m.status IN ('queued','running')
   AND m.github_review_id IS NULL AND a.review_scope='full' AND a.status='running' AND a.result_origin IS NULL
   AND a.publication_state='not_started' AND a.publication_receipt IS NULL AND a.github_review_id IS NULL
   AND NOT EXISTS(SELECT 1 FROM code_review_revision_assessments newer WHERE newer.org_id=a.org_id AND newer.pull_request_id=a.pull_request_id AND newer.generation>a.generation)
   FOR UPDATE OF a,m`, orgID, in.SessionID, in.MetadataID, in.RepositoryID, in.PullRequestID, in.PolicyID, in.HeadSHA, in.OutputKey).Scan(&assessmentID)
			if err != nil {
				return fmt.Errorf("ended full review identity changed: %w", err)
			}
			if state.ActiveSessionID == nil || *state.ActiveSessionID != in.SessionID || (state.ActiveAssessmentID != nil && *state.ActiveAssessmentID != assessmentID) {
				return ErrCodeReviewAssessmentState
			}
			results, err := reviews.ListAgentResults(unitCtx, orgID, in.SessionID)
			if err != nil {
				return err
			}
			if in.SessionStatus == models.SessionStatusCompleted {
				if canResume == nil {
					return ErrCodeReviewAssessmentState
				}
				if canResume(results) {
					resumable = true
					return nil
				}
			}
			reason = strings.TrimSpace(reason)
			if reason == "" {
				reason = "parent code review session ended before validated reviewer and synthesis results were saved"
			}
			if _, err := reviews.FailReviewWithStatus(unitCtx, orgID, FailCodeReviewParams{SessionID: in.SessionID, Reason: reason, Code: models.CodeReviewStatusCodeReviewerFailed, Message: "Review execution ended before usable output was saved. Retry the review to start a fresh attempt.", Retryable: true}); err != nil {
				return err
			}
			if _, err := tx.Exec(unitCtx, `UPDATE code_review_revision_assessments SET status='failed',completed_at=now(),failure_detail=$3 WHERE org_id=$1 AND id=$2 AND status='running'`, orgID, assessmentID, reason); err != nil {
				return err
			}
			if _, err := tx.Exec(unitCtx, `UPDATE code_review_requests SET status='failed' WHERE org_id=$1 AND pull_request_id=$2 AND (assessment_id=$4 OR (session_id=$3 AND assessment_id IS NULL)) AND status IN ('pending','joined')`, orgID, in.PullRequestID, in.SessionID, assessmentID); err != nil {
				return err
			}
			key := "code_review_status_comment:" + in.SessionID.String() + ":terminal"
			payload := map[string]uuid.UUID{"org_id": orgID, "repository_id": in.RepositoryID, "pull_request_id": in.PullRequestID, "session_id": in.SessionID}
			if in.TriggeringDisputeID != nil {
				payload["triggering_dispute_id"] = *in.TriggeringDisputeID
			}
			if _, err := enqueueOn(unitCtx, tx, orgID, EnqueueOpts{Queue: "default", JobType: models.JobTypeSyncCodeReviewStatusComment, Payload: payload, Priority: 3, DedupeKey: &key, MaxAttempts: 3}); err != nil {
				return fmt.Errorf("enqueue ended review status comment: %w", err)
			}
			// Legacy full controllers reserved only a session, before the assessment
			// was linked at completion. Release only this exact session reservation.
			if state.ActiveSessionID != nil && *state.ActiveSessionID == in.SessionID {
				state.ActiveAssessmentID = nil
				state.ActiveSessionID = nil
				if state.PendingInput == nil {
					state.State = models.CodeReviewScheduleIdle
					state.WaitReason = models.CodeReviewWaitNone
					state.RetryAt = nil
					state.EligibleAt = nil
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		staged, called = state, true
		return nil
	})
	if err != nil {
		return false, err
	}
	if !called {
		return false, ErrCodeReviewMetadataTerminalCapability
	}
	// Preserve the existing scheduling signal, but only after the supplied Run
	// reports known commit. The tx-bound metadata store emits no signal itself.
	if s.streams != nil {
		if err := s.streams.PublishUpdated(ctx, orgID, models.CodeReviewUpdatedEvent{OrgID: orgID, PullRequestID: &in.PullRequestID, SchedulingState: staged.State, UpdatedAt: time.Now().UTC()}); err != nil {
			s.logger.Warn().Err(err).Str("pull_request_id", in.PullRequestID.String()).Msg("publish scheduling update failed")
		}
	}
	return resumable, nil
}
