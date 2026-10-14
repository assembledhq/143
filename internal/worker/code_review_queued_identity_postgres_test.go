package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type queuedIdentitySnapshot struct {
	snapshot ghservice.CodeReviewPullRequestSnapshot
	err      error
	hook     func()
}

func (s *queuedIdentitySnapshot) PrepareCodeReviewPullRequestSnapshot(context.Context, uuid.UUID, uuid.UUID) (ghservice.CodeReviewPullRequestSnapshotReader, error) {
	return func(context.Context, int) (ghservice.CodeReviewPullRequestSnapshot, error) {
		if s.hook != nil {
			s.hook()
		}
		return s.snapshot, s.err
	}, nil
}

type queuedIdentityCapture struct {
	result codereviewsvc.AssessmentInputCaptureResult
	hook   func()
	calls  int
}

func (s *queuedIdentityCapture) CaptureAssessmentInputs(context.Context, codereviewsvc.AssessmentInputCaptureRequest) (codereviewsvc.AssessmentInputCaptureResult, error) {
	s.calls++
	if s.hook != nil {
		s.hook()
	}
	return s.result, nil
}

type queuedIdentityJobs struct {
	*db.JobStore
	err error
}

func (s *queuedIdentityJobs) EnqueueWithOpts(ctx context.Context, org uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
	if s.err != nil {
		return uuid.Nil, s.err
	}
	return s.JobStore.EnqueueWithOpts(ctx, org, opts)
}

func TestFullAssessmentQueuedIdentityRecoveryPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for durable queued identity recovery proof")
	}
	ctx := context.Background()
	pool := fullRecoveryPostgresPool(t, ctx)
	tests := []struct {
		name            string
		scheduled       bool
		field           string
		fail            bool
		cancel          bool
		admissionCancel bool
		retired         bool
		gate            string
	}{
		{name: "policy save with scheduling", scheduled: true, field: "policy"},
		{name: "policy save without scheduling", field: "policy"},
		{name: "base moves during capture", scheduled: true, field: "base"},
		{name: "head moves during capture", scheduled: true, field: "head"},
		{name: "legacy base moves during capture", field: "base"},
		{name: "legacy head moves during capture", field: "head"},
		{name: "scheduled admission failure preserves last attempt", scheduled: true, field: "policy", fail: true},
		{name: "legacy queue failure preserves last attempt", field: "policy", fail: true},
		{name: "cancellation during capture does not restart", scheduled: true, field: "policy", cancel: true},
		{name: "cancellation during admission snapshot does not restart", scheduled: true, field: "policy", cancel: true, admissionCancel: true},
		{name: "another request retires source during capture", scheduled: true, field: "policy", retired: true},
		{name: "automatic recovery remains paused", scheduled: true, field: "policy", gate: "paused"},
		{name: "automatic recovery respects re-review disabled", scheduled: true, field: "policy", gate: "disabled"},
		{name: "automatic recovery stops after approval", scheduled: true, field: "policy", gate: "approved"},
		{name: "legacy automatic recovery stops after approval", field: "policy", gate: "approved"},
		{name: "legacy reviewer trigger preserves automatic sync authorization", field: "policy", gate: "legacy_approved"},
		{name: "human recovery proceeds while automatic review paused", scheduled: true, field: "policy", gate: "human_paused"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedStrandedReview(t, pool, "idle")
			f.job.TriggeringDisputeID = nil
			f.job.RequestedReviewerLogin, f.job.RequestedTeamSlug = "143-code-reviewer", "reviewers"
			f.job.RequestContext = &codereviewsvc.ReviewRequestContext{Source: "issue_comment", AuthorLogin: "reviewer", Body: "Check save behavior", URL: "https://example.invalid/comment/1"}
			if tt.gate != "" && tt.gate != "human_paused" && tt.gate != "legacy_approved" {
				_, err := pool.Exec(ctx, `UPDATE code_review_session_metadata SET trigger_source='auto_policy' WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID)
				require.NoError(t, err, "preserve an automatically admitted source review")
			}
			if tt.gate == "paused" || tt.gate == "human_paused" {
				_, err := pool.Exec(ctx, `UPDATE code_review_pr_state SET automatic_paused=true WHERE org_id=$1 AND pull_request_id=$2`, f.job.OrgID, f.job.PullRequestID)
				require.NoError(t, err, "pause only automatic follow-up reviews")
			}
			if tt.gate == "approved" || tt.gate == "legacy_approved" {
				prior := uuid.New()
				_, err := pool.Exec(ctx, `INSERT INTO sessions(id,org_id,origin,status) VALUES($1,$2,'code_review','completed')`, prior, f.job.OrgID)
				require.NoError(t, err, "seed a previously approved parent")
				_, err = pool.Exec(ctx, `INSERT INTO code_review_session_metadata(org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,review_output_key,decision,acceptable,github_review_id,created_at) VALUES($1,$2,$3,$4,$5,'base','head','auto_policy','completed',$6,'approved',true,77,now()-interval '1 minute')`, f.job.OrgID, prior, f.job.RepositoryID, f.job.PullRequestID, f.job.PolicyID, "prior-approved:"+prior.String())
				require.NoError(t, err, "retain durable approval as the automatic stop gate")
			}
			// There is no assessment or reviewer result yet: this is the exact
			// queue-to-capture gap in which mutable policy edits caused failures.
			_, err := pool.Exec(ctx, `DELETE FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
			require.NoError(t, err, "remove the unstarted assessment fixture")
			revisionValues := map[string]any{"request_context": f.job.RequestContext, "pull_request_title": "Saved review"}
			if tt.gate == "legacy_approved" {
				revisionValues["change_reason"] = "pull_request.synchronize"
				_, err := pool.Exec(ctx, `UPDATE code_review_session_metadata SET trigger_source='team_reviewer' WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID)
				require.NoError(t, err, "legacy sync should preserve reviewer trigger separately from automatic authorization")
			}
			revision, err := json.Marshal(revisionValues)
			require.NoError(t, err, "encode immutable human request provenance")
			_, err = pool.Exec(ctx, `UPDATE sessions SET revision_context=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID, revision)
			require.NoError(t, err, "retain original request context in the parent")
			pr, err := f.stores.PullRequests.GetByID(ctx, f.job.OrgID, f.job.PullRequestID)
			require.NoError(t, err, "read the source PR")
			roster, err := json.Marshal(models.DefaultCodeReviewPolicyConfig().AgentRoster)
			require.NoError(t, err, "encode a valid source reviewer and orchestrator roster")
			_, err = pool.Exec(ctx, `UPDATE code_review_policies SET agent_roster=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.PolicyID, roster)
			require.NoError(t, err, "make the source fixture policy dispatchable")
			policy, err := f.stores.CodeReviews.GetPolicyByID(ctx, f.job.OrgID, f.job.PolicyID)
			require.NoError(t, err, "read the immutable queued policy")
			if tt.field == "policy" {
				config := policy.Config()
				config.ReviewInstructions = "Check the updated policy"
				if tt.gate == "disabled" {
					config.SchedulingPolicy = &models.CodeReviewSchedulingPolicy{AutomaticReReview: new(false)}
				}
				policy, err = f.stores.CodeReviews.SavePolicy(ctx, f.job.OrgID, config, nil)
				require.NoError(t, err, "save a new policy after the controller was queued")
			}
			snapshot := &queuedIdentitySnapshot{snapshot: ghservice.CodeReviewPullRequestSnapshot{Number: pr.GitHubPRNumber, HTMLURL: pr.GitHubPRURL, Title: pr.Title, AuthorLogin: "author", State: "open", HeadSHA: f.job.HeadSHA, BaseSHA: "base", BaseRef: "main"}}
			capture := &queuedIdentityCapture{result: codereviewsvc.AssessmentInputCaptureResult{Policy: policy, PullRequest: pr, Snapshot: snapshot.snapshot}}
			if tt.field == "base" {
				capture.result.Snapshot.BaseSHA = "new-base"
			}
			if tt.field == "head" {
				capture.result.Snapshot.HeadSHA = "new-head"
			}
			jobs := &queuedIdentityJobs{JobStore: f.stores.Jobs}
			lifecycle := codereviewsvc.NewService(f.stores.CodeReviews, f.stores.CodeReviews, f.stores.Sessions, jobs, zerolog.Nop(), codereviewsvc.Config{})
			if tt.scheduled {
				lifecycle.SetScheduling(db.NewCodeReviewScheduleStore(pool), snapshot)
			}
			capture.hook = func() {
				snapshot.snapshot = capture.result.Snapshot
				if tt.fail {
					jobs.err = errors.New("replacement queue unavailable")
					snapshot.err = errors.New("replacement snapshot unavailable")
				}
				if tt.cancel {
					cancelParent := func() {
						require.NoError(t, f.stores.Sessions.UpdateStatus(ctx, f.job.OrgID, f.job.SessionID, models.SessionStatusCancelled), "cancel only the original parent during provider capture")
					}
					if tt.admissionCancel {
						snapshot.hook = cancelParent
					} else {
						cancelParent()
					}
				}
				if tt.retired {
					_, err := f.stores.CodeReviews.MarkActiveReviewStale(ctx, f.job.OrgID, f.job.SessionID, "a newer human request owns this PR")
					require.NoError(t, err, "retire the old controller while its source capture was in flight")
					independent := codereviewsvc.NewService(f.stores.CodeReviews, f.stores.CodeReviews, f.stores.Sessions, jobs, zerolog.Nop(), codereviewsvc.Config{})
					result, err := independent.HandleReviewChanged(ctx, codereviewsvc.ReviewChangedInput{OrgID: f.job.OrgID, RepositoryID: f.job.RepositoryID, PullRequestID: f.job.PullRequestID, HeadSHA: f.job.HeadSHA, BaseSHA: "base", GitHubRepo: pr.GitHubRepo, GitHubPRNumber: pr.GitHubPRNumber, GitHubPRURL: pr.GitHubPRURL, PullRequestTitle: pr.Title, ExplicitRequest: true, GitHubDeliveryID: "independent-request:" + f.job.SessionID.String(), TriggerSource: models.CodeReviewTriggerSourceSlashCommand})
					require.NoError(t, err, "admit an independent newer full review")
					require.True(t, result.Processed, "new independent request should own the PR")
				}
			}
			services := &Services{CodeReviewAssessmentsEnabled: true, CodeReviewInputCapture: capture, CodeReviewLifecycle: lifecycle}
			node := "identity-recovery-" + uuid.NewString()
			_, err = pool.Exec(ctx, `INSERT INTO nodes(id,mode) VALUES($1,'worker')`, node)
			require.NoError(t, err, "register isolated recovery worker")
			payload, err := json.Marshal(f.job)
			require.NoError(t, err, "encode the immutable queued job")
			_, err = pool.Exec(ctx, `UPDATE jobs SET payload=$3,status='pending',lock_token=NULL,attempts=7,max_attempts=8,target_node_id=$4,priority=100000,run_at=now() WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID, payload, node)
			require.NoError(t, err, "reproduce the eighth controller claim")
			worker := New(pool, zerolog.Nop(), node)
			worker.Register(models.JobTypeRunCodeReview, newRunCodeReviewHandler(f.stores, services, zerolog.Nop()))
			worker.poll(ctx)
			if tt.fail {
				old, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
				require.NoError(t, err, "read source after admission failure")
				require.Equal(t, models.CodeReviewSessionStatusRunning, old.Status, "failed admission must not retire the only recoverable review")
				var status models.JobStatus
				var attempts int
				require.NoError(t, pool.QueryRow(ctx, `SELECT status,attempts FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID).Scan(&status, &attempts), "read the eighth claim after failed handoff")
				require.Equal(t, models.JobStatusPending, status, "failed durable admission must defer rather than dead-letter the obsolete identity")
				require.Equal(t, 7, attempts, "recovery dependencies must preserve the ordinary controller attempt budget")
				jobs.err, snapshot.err, capture.hook = nil, nil, nil
				_, err = pool.Exec(ctx, `UPDATE jobs SET run_at=now() WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID)
				require.NoError(t, err, "advance recovered handoff without sleeping")
				worker.poll(ctx)
			}
			var jobStatus models.JobStatus
			require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID).Scan(&jobStatus), "read obsolete controller settlement")
			require.Equal(t, models.JobStatusSucceeded, jobStatus, "source drift must finish as superseded work on its eighth claim")
			old, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read settled immutable source metadata")
			require.Equal(t, f.job.PolicyID, old.PolicyID, "recovery must retain the originally queued policy identity")
			require.Equal(t, f.job.HeadSHA, old.HeadSHA, "recovery must retain the originally queued head")
			if tt.cancel {
				require.Equal(t, models.CodeReviewSessionStatusCancelled, old.Status, "parent cancellation must win over replacement recovery")
				var replacementCount int
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type IN ('start_code_review_reassessment','reconcile_code_review_schedule')`, f.job.OrgID).Scan(&replacementCount), "count replacement work after cancellation")
				require.Equal(t, 0, replacementCount, "cancellation during capture must not create a replacement")
				_, err = f.stores.CodeReviews.MarkActiveReviewStale(ctx, f.job.OrgID, f.job.SessionID, "late stale callback")
				require.ErrorIs(t, err, pgx.ErrNoRows, "late retirement must not overwrite a concurrent cancellation")
				return
			}
			require.Equal(t, models.CodeReviewSessionStatusStale, old.Status, "durably accepted replacement must retire obsolete work")
			if tt.retired {
				parent, err := f.stores.Sessions.GetByID(ctx, f.job.OrgID, f.job.SessionID)
				require.NoError(t, err, "read retired parent without reviewer threads")
				require.Equal(t, models.SessionStatusCompleted, parent.Status, "an obsolete no-thread parent must settle when its controller acknowledges retirement")
				var recoveryRequests int
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM code_review_requests WHERE org_id=$1 AND source_identity=$2`, f.job.OrgID, "capture_identity:"+f.job.SessionID.String()).Scan(&recoveryRequests), "count force requests from the retired source")
				require.Equal(t, 0, recoveryRequests, "late old capture must not force a new request or revoke the newer owner")
				latest, err := f.stores.CodeReviews.GetLatestByPullRequest(ctx, f.job.OrgID, f.job.PullRequestID)
				require.NoError(t, err, "read the independent newer owner")
				require.NotEqual(t, f.job.SessionID, latest.SessionID, "independent request must retain its immutable new session")
				require.Equal(t, models.CodeReviewSessionStatusQueued, latest.Status, "late old capture must leave the new review queued")
				return
			}
			if tt.gate != "" && tt.gate != "human_paused" {
				var count int
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type='run_code_review'`, f.job.OrgID).Scan(&count), "count controllers after automatic admission gate")
				require.Equal(t, 1, count, "automatic recovery must not dispatch past pause, disabled re-review or approval")
				if tt.scheduled {
					if tt.gate != "approved" {
						var wake uuid.UUID
						lease := uuid.New()
						require.NoError(t, pool.QueryRow(ctx, `UPDATE jobs SET status='running',lock_token=$2,lease_expires_at=now()+interval '5 minutes' WHERE org_id=$1 AND job_type='reconcile_code_review_schedule' RETURNING id`, f.job.OrgID, lease).Scan(&wake), "claim the paused automatic recovery wake")
						require.NoError(t, lifecycle.ReconcileSchedule(jobctx.WithLockToken(jobctx.WithJobID(ctx, wake), lease), models.CodeReviewScheduleWake{OrgID: f.job.OrgID, PullRequestID: f.job.PullRequestID}), "rehydrating pending recovery must preserve automatic authorization")
					}
					state, err := lifecycle.GetSchedule(ctx, f.job.OrgID, f.job.PullRequestID)
					require.NoError(t, err, "read automatic recovery hold")
					expectedReason := models.CodeReviewWaitPaused
					if tt.gate == "approved" {
						expectedReason = models.CodeReviewWaitApproved
						require.Nil(t, state.PendingInput, "prior approval must clear automatic replacement intent")
					} else {
						require.NotNil(t, state.PendingInput, "pause must retain recoverable replacement intent")
					}
					require.Equal(t, expectedReason, state.WaitReason, "source trigger must preserve the existing automatic stop gate")
				}
				return
			}
			var replacementType string
			var replacementID uuid.UUID
			var replacementPayload json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT id,job_type,payload FROM jobs WHERE org_id=$1 AND job_type IN ('start_code_review_reassessment','reconcile_code_review_schedule')`, f.job.OrgID).Scan(&replacementID, &replacementType, &replacementPayload), "there should be exactly one durable replacement job")
			if tt.scheduled {
				require.Equal(t, models.JobTypeReconcileCodeReviewSchedule, replacementType, "enabled scheduling must retain a durable current-input wake")
				lease := uuid.New()
				_, err = pool.Exec(ctx, `UPDATE jobs SET status='running',lock_token=$3,lease_expires_at=now()+interval '5 minutes' WHERE org_id=$1 AND id=$2`, f.job.OrgID, replacementID, lease)
				require.NoError(t, err, "claim only this replacement wake")
				require.NoError(t, lifecycle.ReconcileSchedule(jobctx.WithLockToken(jobctx.WithJobID(ctx, replacementID), lease), models.CodeReviewScheduleWake{OrgID: f.job.OrgID, PullRequestID: f.job.PullRequestID}), "durable handoff should dispatch the current full review")
			} else {
				require.Equal(t, models.JobTypeStartCodeReviewReassessment, replacementType, "disabled scheduling must use its supported durable starter")
				var input codereviewsvc.ReviewChangedInput
				require.NoError(t, json.Unmarshal(replacementPayload, &input), "restore accepted legacy replacement intent")
				result, err := lifecycle.HandleReviewChanged(ctx, input)
				require.NoError(t, err, "the legacy starter must dispatch after old work retires")
				require.True(t, result.Processed, "legacy current-input full review should be accepted")
			}
			latest, err := f.stores.CodeReviews.GetLatestByPullRequest(ctx, f.job.OrgID, f.job.PullRequestID)
			require.NoError(t, err, "read dispatched current-input replacement")
			require.NotEqual(t, f.job.SessionID, latest.SessionID, "replacement must be a new immutable session")
			require.Equal(t, policy.ID, latest.PolicyID, "replacement must resolve the currently active policy")
			require.Equal(t, capture.result.Snapshot.HeadSHA, latest.HeadSHA, "replacement must target the authoritative provider head")
			require.Equal(t, capture.result.Snapshot.BaseSHA, latest.BaseSHA, "replacement must target the authoritative provider base")
			var currentPayload codereviewsvc.RunCodeReviewJobPayload
			var raw json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT payload FROM jobs WHERE org_id=$1 AND job_type='run_code_review' AND payload->>'session_id'=$2`, f.job.OrgID, latest.SessionID.String()).Scan(&raw), "read the distinct current-policy controller")
			require.NoError(t, json.Unmarshal(raw, &currentPayload), "decode current controller provenance")
			require.Equal(t, f.job.RequestContext, currentPayload.RequestContext, "replacement must preserve the original human request")
			require.Equal(t, f.job.RequestedReviewerLogin, currentPayload.RequestedReviewerLogin, "replacement must preserve reviewer cleanup identity")
			require.Equal(t, f.job.RequestedTeamSlug, currentPayload.RequestedTeamSlug, "replacement must preserve reviewer team identity")
			// Replay both the retired controller and the recovery handoff after a
			// newer attempt is active. Neither may revoke or duplicate that attempt.
			require.NoError(t, newRunCodeReviewHandler(f.stores, services, zerolog.Nop())(ctx, "replay", payload), "retired controller replay should stop before capture")
			metadata := old
			require.NoError(t, supersedeCodeReviewForCapturedIdentity(ctx, f.stores, services, zerolog.Nop(), f.job, metadata, capture.result, &fullAssessmentQueuedIdentityChanged{Fields: []string{tt.field}}), "replayed handoff must join the newer replacement")
			current, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, latest.SessionID)
			require.NoError(t, err, "read replacement after old handoff replay")
			require.Equal(t, models.CodeReviewSessionStatusQueued, current.Status, "an obsolete replay must not force-revoke the newer full review")
			var counts []int
			require.NoError(t, pool.QueryRow(ctx, `SELECT ARRAY[(SELECT count(*)::int FROM code_review_session_metadata WHERE org_id=$1),(SELECT count(*)::int FROM code_review_revision_assessments WHERE org_id=$1),(SELECT count(*)::int FROM code_review_agent_results WHERE org_id=$1),(SELECT count(*)::int FROM jobs WHERE org_id=$1 AND job_type IN ('run_agent','continue_session','prepare_code_review_workspace'))]`, f.job.OrgID).Scan(&counts), "count immutable attempts and fanout after replay")
			require.Equal(t, []int{2, 0, 0, 0}, counts, "recovery must create one replacement without assessing or fanning out obsolete work")
		})
	}
}
