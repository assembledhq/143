package db

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type captureHandoffSettlementFixture struct {
	orgID, repositoryID, pullRequestID, policyID uuid.UUID
	sourceSessionID, replacementSessionID        uuid.UUID
	disputeID, requestID                         uuid.UUID
}

func seedCaptureHandoffSettlement(t *testing.T, pool *pgxpool.Pool) captureHandoffSettlementFixture {
	t.Helper()
	ctx := context.Background()
	f := captureHandoffSettlementFixture{
		orgID: uuid.New(), repositoryID: uuid.New(), pullRequestID: uuid.New(), policyID: uuid.New(),
		sourceSessionID: uuid.New(), replacementSessionID: uuid.New(), disputeID: uuid.New(), requestID: uuid.New(),
	}
	integrationID, reviewedSessionID := uuid.New(), uuid.New()
	for _, row := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO organizations(id,name) VALUES($1,'Capture Handoff Settlement Test')`, []any{f.orgID}},
		{`INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, []any{integrationID, f.orgID}},
		{`INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,(hashtextextended($1::uuid::text,0) & 9223372036854775807),'test/handoff','https://example.invalid/repo.git',1)`, []any{f.repositoryID, f.orgID, integrationID}},
		{`INSERT INTO sessions(id,org_id,origin,status) VALUES($1,$4,'code_review','completed'),($2,$4,'code_review','completed'),($3,$4,'code_review','idle')`, []any{reviewedSessionID, f.sourceSessionID, f.replacementSessionID, f.orgID}},
		{`INSERT INTO pull_requests(id,org_id,github_pr_number,github_pr_url,github_repo,title,head_sha,base_sha) VALUES($1,$2,9,'https://example.invalid/pr/9','test/handoff','Synthetic review','head','base')`, []any{f.pullRequestID, f.orgID}},
		{`INSERT INTO code_review_policies(id,org_id,version,approval_mode,description_policy,risk_policy,agent_roster,review_instructions,automated_approval_policy,continuation_policy) VALUES($1,$2,1,'approve_acceptable','{}','{}','{}','','','{}')`, []any{f.policyID, f.orgID}},
		{`INSERT INTO code_review_decision_disputes(id,org_id,session_id,pull_request_id,repository_id,policy_id,reviewed_head_sha,decision,source,source_body_hash,body,semantic_input_hash_at_filing,direction,routing,asserts_new_information,intake_status,reassessment_status,reassessment_session_id,reassessment_decision,reassessment_flipped,reply_status)
 VALUES($1,$2,$3,$4,$5,$6,'head','blocked','github_comment','synthetic-body','Please reconsider.','synthetic-input','should_have_approved','reassess',true,'triaged','running',$7,'blocked',false,'failed')`, []any{f.disputeID, f.orgID, reviewedSessionID, f.pullRequestID, f.repositoryID, f.policyID, f.sourceSessionID}},
		{`INSERT INTO code_review_session_metadata(org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,review_output_key,triggering_dispute_id) VALUES($1,$2,$3,$4,$5,'base','head','dispute_reassessment','stale',$6,$7)`, []any{f.orgID, f.sourceSessionID, f.repositoryID, f.pullRequestID, f.policyID, "synthetic-source:" + f.sourceSessionID.String(), f.disputeID}},
		{`INSERT INTO code_review_requests(id,org_id,repository_id,pull_request_id,source_kind,source_identity,mode,input_hash,target_generation,status) VALUES($1,$2,$3,$4,'github',$5,'review_now','synthetic-input',1,'pending')`, []any{f.requestID, f.orgID, f.repositoryID, f.pullRequestID, "capture_identity:" + f.sourceSessionID.String()}},
	} {
		_, err := pool.Exec(ctx, row.query, row.args...)
		require.NoError(t, err, "seed a full-schema synthetic capture handoff relationship")
	}
	return f
}

func TestRetiredCapturedIdentityReassessmentSettlementPostgres(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	ctx := context.Background()
	tests := []struct {
		name, requestStatus, owner, marker, fence, reply, source, direction, legacyStatus string
		completed, settled                                                                bool
	}{
		{name: "cancelled request settles old owner", requestStatus: "cancelled", settled: true},
		{name: "superseded request settles unbound owner through legacy marker", requestStatus: "superseded", owner: "null", marker: "legacy", settled: true},
		{name: "unrelated satisfied request settles old owner", requestStatus: "satisfied", settled: true},
		{name: "cancelled request retains already published reply", requestStatus: "cancelled", reply: "published", settled: true},
		{name: "application objection preserves inapplicable reply", requestStatus: "cancelled", source: "app_ui", reply: "not_applicable", settled: true},
		{name: "approval objection requires reply on any source", requestStatus: "superseded", source: "app_ui", direction: "should_not_have_approved", settled: true},
		{name: "live legacy starter preserves recovery after scheduled retirement", requestStatus: "cancelled", legacyStatus: "pending"},
		{name: "terminal legacy starter permits settlement after scheduled retirement", requestStatus: "superseded", legacyStatus: "dead_letter", settled: true},
		{name: "new replacement owner is preserved", requestStatus: "superseded", owner: "replacement"},
		{name: "completed reassessment is preserved", requestStatus: "cancelled", completed: true},
		{name: "other organization is preserved", requestStatus: "cancelled", fence: "org"},
		{name: "other pull request is preserved", requestStatus: "cancelled", fence: "pr"},
		{name: "unrelated dispute authorization is preserved", requestStatus: "cancelled", marker: "missing"},
		{name: "pending request retains reassessment", requestStatus: "pending"},
		{name: "same source satisfied request is preserved", requestStatus: "satisfied", owner: "same-satisfied"},
		{name: "malformed source identity is harmless", requestStatus: "cancelled", fence: "malformed"},
		{name: "non GitHub request is preserved", requestStatus: "cancelled", fence: "kind"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedCaptureHandoffSettlement(t, pool)
			callOrgID, callPRID := f.orgID, f.pullRequestID
			var satisfiedSessionID *uuid.UUID
			if tt.requestStatus == "satisfied" {
				satisfiedSessionID = &f.replacementSessionID
				if tt.owner == "same-satisfied" {
					satisfiedSessionID = &f.sourceSessionID
				}
			}
			_, err := pool.Exec(ctx, `UPDATE code_review_requests SET status=$3,session_id=$4 WHERE org_id=$1 AND id=$2`, f.orgID, f.requestID, tt.requestStatus, satisfiedSessionID)
			require.NoError(t, err, "apply the concrete replacement request retirement state")
			switch tt.owner {
			case "null":
				_, err = pool.Exec(ctx, `UPDATE code_review_decision_disputes SET reassessment_session_id=NULL,reassessment_status='queued' WHERE org_id=$1 AND id=$2`, f.orgID, f.disputeID)
			case "replacement":
				_, err = pool.Exec(ctx, `UPDATE code_review_decision_disputes SET reassessment_session_id=$3 WHERE org_id=$1 AND id=$2`, f.orgID, f.disputeID, f.replacementSessionID)
			}
			require.NoError(t, err, "apply the actual reassessment ownership state")
			if tt.completed {
				_, err = pool.Exec(ctx, `UPDATE code_review_decision_disputes SET reassessment_status='completed',reassessment_decision='approved',reassessment_flipped=true WHERE org_id=$1 AND id=$2`, f.orgID, f.disputeID)
				require.NoError(t, err, "retain the previously completed exact decision")
			}
			if tt.reply != "" {
				_, err = pool.Exec(ctx, `UPDATE code_review_decision_disputes SET reply_status=$3,reply_comment_id=41 WHERE org_id=$1 AND id=$2`, f.orgID, f.disputeID, tt.reply)
				require.NoError(t, err, "retain the already published reply identity")
			}
			if tt.marker != "" {
				_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET triggering_dispute_id=NULL WHERE org_id=$1 AND session_id=$2`, f.orgID, f.sourceSessionID)
				require.NoError(t, err, "exercise source authorization independently of the metadata field")
				if tt.marker == "legacy" {
					_, err = pool.Exec(ctx, `UPDATE sessions SET revision_context=jsonb_build_object('triggering_dispute_id',$3::text) WHERE org_id=$1 AND id=$2`, f.orgID, f.sourceSessionID, f.disputeID.String())
					require.NoError(t, err, "retain the legacy immutable revision-context dispute marker")
				}
			}
			if tt.source != "" {
				_, err = pool.Exec(ctx, `UPDATE code_review_decision_disputes SET source=$3 WHERE org_id=$1 AND id=$2`, f.orgID, f.disputeID, tt.source)
				require.NoError(t, err, "apply the actual dispute source reply semantics")
			}
			if tt.direction != "" {
				_, err = pool.Exec(ctx, `UPDATE code_review_decision_disputes SET direction=$3 WHERE org_id=$1 AND id=$2`, f.orgID, f.disputeID, tt.direction)
				require.NoError(t, err, "apply a dispute direction that independently requires a reply")
			}
			if tt.legacyStatus != "" {
				_, err = pool.Exec(ctx, `INSERT INTO jobs(org_id,queue,job_type,payload,dedupe_key,status) VALUES($1,'agent','start_code_review_reassessment',jsonb_build_object('org_id',$1::uuid::text,'triggering_dispute_id',$2::uuid::text),$3,$4)`, f.orgID, f.disputeID, "code_review_reassessment:"+f.pullRequestID.String()+":capture_identity:"+f.sourceSessionID.String(), tt.legacyStatus)
				require.NoError(t, err, "retain the same-source legacy recovery lifecycle alongside a retired scheduled request")
			}
			switch tt.fence {
			case "org":
				callOrgID = uuid.New()
			case "pr":
				callPRID = uuid.New()
			case "malformed":
				_, err = pool.Exec(ctx, `UPDATE code_review_requests SET source_identity='capture_identity:not-a-uuid' WHERE org_id=$1 AND id=$2`, f.orgID, f.requestID)
			case "kind":
				_, err = pool.Exec(ctx, `UPDATE code_review_requests SET source_kind='api' WHERE org_id=$1 AND id=$2`, f.orgID, f.requestID)
			}
			require.NoError(t, err, "apply the tenant, pull request, or source identity fence")
			store := NewCodeReviewDisputeStore(pool)
			before, err := store.GetByID(ctx, f.orgID, f.disputeID)
			require.NoError(t, err, "read every dispute field before settlement")
			tx, err := pool.Begin(ctx)
			require.NoError(t, err, "begin the caller's pull request transaction")
			defer func() { _ = tx.Rollback(ctx) }()
			var transitionAt time.Time
			require.NoError(t, tx.QueryRow(ctx, `SELECT now()`).Scan(&transitionAt), "capture the transaction timestamp for exact durable comparison")
			require.NoError(t, SettleRetiredCapturedIdentityReassessments(ctx, tx, callOrgID, callPRID), "settlement must tolerate malformed unrelated identities and preserve ownership fences")
			require.NoError(t, tx.Commit(ctx), "commit dispute transition together with its reply job")
			after, err := store.GetByID(ctx, f.orgID, f.disputeID)
			require.NoError(t, err, "read the full durable dispute after settlement")
			expected := before
			if tt.settled {
				expected.ReassessmentSessionID = &f.sourceSessionID
				expected.ReassessmentStatus = models.CodeReviewDisputeReassessmentFailed
				expected.ReassessmentDecision, expected.ReassessmentFlipped = nil, nil
				detail := "The reassessment was stopped before a replacement review could start. The objection remains recorded for a policy owner."
				expected.StatusDetail = &detail
				if (expected.Source == models.CodeReviewDisputeSourceGitHubComment || expected.Direction != nil && *expected.Direction == models.CodeReviewDisputeDirectionShouldNotHaveApproved) && expected.ReplyStatus != models.CodeReviewDisputeReplyPublished {
					expected.ReplyStatus = models.CodeReviewDisputeReplyPending
				}
				expected.UpdatedAt, expected.Version = transitionAt, before.Version+1
			}
			require.Equal(t, expected, after, "only the abandoned old or unbound reassessment may transition and every other durable field must survive")
			type replyJob struct {
				Queue, JobType, DedupeKey, Status string
				Payload                           map[string]string
				Priority, MaxAttempts             int
			}
			rows, err := pool.Query(ctx, `SELECT queue,job_type,dedupe_key,status,payload,priority,max_attempts FROM jobs WHERE org_id=$1 AND job_type='reply_code_review_dispute' ORDER BY id`, f.orgID)
			require.NoError(t, err, "read only reply jobs emitted for this synthetic tenant")
			jobs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (replyJob, error) {
				var job replyJob
				var raw []byte
				if err := row.Scan(&job.Queue, &job.JobType, &job.DedupeKey, &job.Status, &raw, &job.Priority, &job.MaxAttempts); err != nil {
					return job, err
				}
				if err := json.Unmarshal(raw, &job.Payload); err != nil {
					return job, err
				}
				return job, nil
			})
			require.NoError(t, err, "decode the emitted reply job contract")
			expectedJobs := []replyJob{}
			if tt.settled {
				expectedJobs = []replyJob{{Queue: "feedback", JobType: models.JobTypeReplyCodeReviewDispute, DedupeKey: "reply_code_review_dispute:" + f.disputeID.String() + ":capture_identity_retired:" + f.sourceSessionID.String(), Status: "pending", Payload: map[string]string{"org_id": f.orgID.String(), "dispute_id": f.disputeID.String()}, Priority: 4, MaxAttempts: 6}}
			}
			require.Equal(t, expectedJobs, jobs, "only actual transitions may emit exactly one durable correctly scoped reply job")
			_, err = pool.Exec(ctx, `UPDATE jobs SET status='succeeded',completed_at=now() WHERE org_id=$1 AND job_type='reply_code_review_dispute'`, f.orgID)
			require.NoError(t, err, "make any reply terminal before replaying settlement")
			replayTx, err := pool.Begin(ctx)
			require.NoError(t, err, "begin independent settlement replay")
			defer func() { _ = replayTx.Rollback(ctx) }()
			require.NoError(t, SettleRetiredCapturedIdentityReassessments(ctx, replayTx, callOrgID, callPRID), "replay must not revive the abandoned reassessment")
			require.NoError(t, replayTx.Commit(ctx), "commit idempotent settlement replay")
			replayed, err := store.GetByID(ctx, f.orgID, f.disputeID)
			require.NoError(t, err, "read the full dispute after replay")
			require.Equal(t, after, replayed, "replay must preserve every settled or fenced dispute field and version")
			var replyCount int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type='reply_code_review_dispute'`, f.orgID).Scan(&replyCount), "count reply history after terminal reply replay")
			require.Equal(t, len(expectedJobs), replyCount, "a terminal reply must not permit another job when no reassessment transitions")
		})
	}
}

func TestRetiredCaptureHandoffResolutionAtomicPostgres(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	ctx := context.Background()
	tests := []struct {
		name string
		fail bool
	}{{name: "request retirement settles and queues reply atomically"}, {name: "reply insertion failure rolls back request and dispute", fail: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedCaptureHandoffSettlement(t, pool)
			store := NewCodeReviewDisputeStore(pool)
			before, err := store.GetByID(ctx, f.orgID, f.disputeID)
			require.NoError(t, err, "read the pending reassessment before transactional request resolution")
			tx, err := pool.Begin(ctx)
			require.NoError(t, err, "begin the real request resolution transaction")
			defer func() { _ = tx.Rollback(ctx) }()
			if tt.fail {
				_, err = tx.Exec(ctx, `CREATE FUNCTION pg_temp.reject_capture_retirement_reply() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.org_id='`+f.orgID.String()+`'::uuid AND NEW.job_type='reply_code_review_dispute' THEN RAISE EXCEPTION 'synthetic capture reply insertion rejected'; END IF; RETURN NEW; END $$`)
				require.NoError(t, err, "install a transaction-local failure for this tenant's reply insertion")
				trigger := "capture_reply_reject_" + strings.ReplaceAll(f.disputeID.String(), "-", "")
				_, err = tx.Exec(ctx, `CREATE TRIGGER `+trigger+` BEFORE INSERT ON jobs FOR EACH ROW EXECUTE FUNCTION pg_temp.reject_capture_retirement_reply()`)
				require.NoError(t, err, "fault the actual reply insertion while leaving request and dispute SQL real")
			}
			err = ResolveCodeReviewRequests(ctx, tx, f.orgID, f.pullRequestID, 1, nil, "cancelled")
			if tt.fail {
				require.ErrorContains(t, err, "synthetic capture reply insertion rejected", "reply failure must propagate from the central request resolution transaction")
				require.NoError(t, tx.Rollback(ctx), "roll back request, dispute, reply, and synthetic trigger together")
			} else {
				require.NoError(t, err, "central request resolution must invoke reassessment settlement")
				require.NoError(t, tx.Commit(ctx), "commit request retirement with both durable follow-up effects")
			}
			var requestStatus string
			require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM code_review_requests WHERE org_id=$1 AND id=$2`, f.orgID, f.requestID).Scan(&requestStatus), "read request status after successful or failed transactional resolution")
			after, err := store.GetByID(ctx, f.orgID, f.disputeID)
			require.NoError(t, err, "read dispute after successful or failed transactional resolution")
			var replyCount int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type='reply_code_review_dispute'`, f.orgID).Scan(&replyCount), "read reply admission after request resolution")
			if tt.fail {
				require.Equal(t, "pending", requestStatus, "reply insertion failure must roll back the replacement request retirement")
				require.Equal(t, before, after, "reply insertion failure must roll back every reassessment field and version")
				require.Equal(t, 0, replyCount, "failed atomic settlement cannot leave a partial reply job")
				return
			}
			require.Equal(t, "cancelled", requestStatus, "successful request retirement must persist its cancelled state")
			require.Equal(t, models.CodeReviewDisputeReassessmentFailed, after.ReassessmentStatus, "central request resolution must settle the abandoned reassessment")
			require.Equal(t, 1, replyCount, "successful atomic settlement must persist the corresponding reply")
		})
	}
}

func TestCaptureHandoffProtectionRequiresLiveWorkPostgres(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	ctx := context.Background()
	tests := []struct {
		name, provider, status string
		protected              bool
	}{
		{name: "pending scheduled request protects handoff", provider: "request", status: "pending", protected: true},
		{name: "joined scheduled request does not protect handoff", provider: "request", status: "joined"},
		{name: "cancelled scheduled request does not protect handoff", provider: "request", status: "cancelled"},
		{name: "superseded scheduled request does not protect handoff", provider: "request", status: "superseded"},
		{name: "satisfied scheduled request does not protect handoff", provider: "request", status: "satisfied"},
		{name: "failed scheduled request does not protect handoff", provider: "request", status: "failed"},
		{name: "pending legacy starter protects handoff", provider: "job", status: "pending", protected: true},
		{name: "running legacy starter protects handoff", provider: "job", status: "running", protected: true},
		{name: "succeeded legacy starter does not protect handoff", provider: "job", status: "succeeded"},
		{name: "dead-lettered legacy starter does not protect handoff", provider: "job", status: "dead_letter"},
		{name: "cancelled legacy starter does not protect handoff", provider: "job", status: "cancelled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedCaptureHandoffSettlement(t, pool)
			if tt.provider == "request" {
				_, err := pool.Exec(ctx, `UPDATE code_review_requests SET status=$3 WHERE org_id=$1 AND id=$2`, f.orgID, f.requestID, tt.status)
				require.NoError(t, err, "apply the scheduled replacement lifecycle state")
			} else {
				_, err := pool.Exec(ctx, `DELETE FROM code_review_requests WHERE org_id=$1 AND id=$2`, f.orgID, f.requestID)
				require.NoError(t, err, "isolate the legacy starter protection path")
				_, err = pool.Exec(ctx, `INSERT INTO jobs(org_id,queue,job_type,payload,dedupe_key,status) VALUES($1,'agent','start_code_review_reassessment',jsonb_build_object('org_id',$1::uuid::text,'triggering_dispute_id',$2::uuid::text),$3,$4)`, f.orgID, f.disputeID, "code_review_reassessment:"+f.pullRequestID.String()+":capture_identity:"+f.sourceSessionID.String(), tt.status)
				require.NoError(t, err, "seed the exact durable legacy handoff and lifecycle state")
			}
			store := NewCodeReviewDisputeStore(pool)
			before, err := store.GetByID(ctx, f.orgID, f.disputeID)
			require.NoError(t, err, "read reassessment before evaluating real SQL handoff protection")
			protected, err := store.IsCapturedIdentityReassessmentSource(ctx, f.orgID, f.disputeID, f.sourceSessionID)
			require.NoError(t, err, "evaluate actual durable replacement handoff protection")
			require.Equal(t, tt.protected, protected, "only pending scheduled requests and pending or running legacy starters may suppress source failure")
			transitioned, err := store.CompleteReassessmentOnce(ctx, f.orgID, f.disputeID, f.sourceSessionID, models.CodeReviewSessionStatusStale, nil, "Synthetic source stopped.")
			require.NoError(t, err, "attempt the actual stale-source reassessment completion")
			require.Equal(t, !tt.protected, transitioned, "terminal replacement work must no longer suppress matching source completion")
			after, err := store.GetByID(ctx, f.orgID, f.disputeID)
			require.NoError(t, err, "read durable reassessment after protection check")
			if tt.protected {
				require.Equal(t, before, after, "live handoff protection must preserve every reassessment field")
			} else {
				require.Equal(t, models.CodeReviewDisputeReassessmentSuperseded, after.ReassessmentStatus, "retired handoff protection must permit matching stale-owner settlement")
				require.Equal(t, &f.sourceSessionID, after.ReassessmentSessionID, "stale-owner settlement must retain its exact source owner")
			}
		})
	}
}
