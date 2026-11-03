package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func admissionPostgresCommand(t *testing.T, f activatedLifecycleFixture, key string, change func(*MessageAdmissionInput)) MessageAdmission {
	t.Helper()
	input := MessageAdmissionInput{OrgID: f.orgID, Fence: f.fence, Message: "Synthetic ordinary input", Dispatch: MessageAdmissionDispatchOptions{RunAt: time.Now().UTC().Truncate(time.Microsecond), MaxAttempts: 3}}
	if change != nil {
		change(&input)
	}
	input.Ingress = prepareMessageAdmissionCommandFixture(t, input, key)
	command, err := NewMessageAdmission(input)
	require.NoError(t, err, "full-schema command must freeze its exact input scope ingress and concrete server options before attempts")
	return command
}

func runAdmissionPostgres(t *testing.T, pool *pgxpool.Pool, command MessageAdmission) (Admission, []LifecycleEffect, error) {
	t.Helper()
	var result AdmissionResult[Admission]
	err := RunLifecycleTransaction(context.Background(), pool, func(ctx context.Context, tx pgx.Tx) error {
		result = AdmissionResult[Admission]{}
		var err error
		result, err = AdmitMessageInTx(ctx, tx, command.OrgID(), command)
		return err
	})
	if err != nil {
		return Admission{}, nil, err
	}
	a, effects, err := result.Unpack()
	return a, effects, err
}

func observeAdmissionPostgres(t *testing.T, pool *pgxpool.Pool, command MessageAdmission) (Admission, bool, error) {
	t.Helper()
	var a Admission
	var found bool
	err := RunLifecycleTransaction(context.Background(), pool, func(ctx context.Context, tx pgx.Tx) error {
		a, found = Admission{}, false
		var err error
		a, found, err = FindMessageAdmissionReplayInTx(ctx, tx, command.OrgID(), command.Fence().sessionID, command.Fence().threadID, command.Ingress())
		return err
	})
	return a, found, err
}

func admissionPostgresExec(t *testing.T, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	require.NoError(t, RunLifecycleTransaction(context.Background(), pool, func(ctx context.Context, tx pgx.Tx) error { _, err := tx.Exec(ctx, query, args...); return err }), "synthetic fixture mutation must use an independently settled configured transaction")
}

func seedMessageAdmissionPendingRequest(t *testing.T, pool *pgxpool.Pool, f activatedLifecycleFixture, lane *uuid.UUID) uuid.UUID {
	t.Helper()
	request := uuid.New()
	admissionPostgresExec(t, pool, `INSERT INTO session_human_input_requests(id,org_id,session_id,thread_id,agent_type,request_kind,title,body) VALUES($1,$2,$3,$4,$5,'free_text','Synthetic pending request','Synthetic pending context')`, request, f.orgID, f.sessionID, lane, models.DefaultDefaultAgentType)
	return request
}

func seedMessageAdmissionRepair(t *testing.T, pool *pgxpool.Pool, f activatedLifecycleFixture, lane *uuid.UUID, active bool, head string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	pr := uuid.New()
	admissionPostgresExec(t, pool, `INSERT INTO pull_requests(id,org_id,session_id,github_pr_number,github_pr_url,github_repo,title,head_sha,base_sha) VALUES($1,$2,$3,9,'https://example.invalid/pr/9','synthetic/admission','Synthetic repair','current-head','base')`, pr, f.orgID, f.sessionID)
	run := models.PullRequestRepairRun{OrgID: f.orgID, PullRequestID: pr, SessionID: f.sessionID, ThreadID: lane, ActionType: models.PullRequestRepairActionTypeFixTests, HealthVersion: 1, HeadSHA: head, BaseSHA: "base", Active: active}
	require.NoError(t, RunLifecycleTransaction(context.Background(), pool, func(ctx context.Context, tx pgx.Tx) error { return NewPullRequestStore(tx).CreateRepairRun(ctx, &run) }), "repair fixture must use the real reservation store inside its independently settled supplied unit")
	return run.ID, pr
}

func seedMessageAdmissionRetainedRepairRuntime(t *testing.T, pool *pgxpool.Pool, f activatedLifecycleFixture, job uuid.UUID) {
	t.Helper()
	runtime := createSeedRuntime(t, pool, f.orgID, f.sessionID, f.primaryThreadID)
	// Retain a complete synthetic allocation on the sibling's own epoch (four),
	// not the target lane's epoch (five). This is work to reconcile, not a typed
	// allocation receipt or permission to launch, settle or release the repair.
	admissionPostgresExec(t, pool, `UPDATE thread_runtimes SET job_id=$3,
 job_attempt_token=gen_random_uuid(),allocation_digest=repeat('a',64),
 allocation_expected_turn=0,input_dispatch_ids=ARRAY[gen_random_uuid()],
 admission_session_epoch=$4,admission_thread_epoch=4,provider_invocation_ordinal=0
 WHERE org_id=$1 AND id=$2`, f.orgID, runtime.ID, job, f.fence.sessionEpoch)
}

type admissionPostgresCounts struct{ messages, inbox, dispatches, jobs, claims, pending int64 }

func admissionPostgresGraph(t *testing.T, q DBTX, f activatedLifecycleFixture) admissionPostgresCounts {
	t.Helper()
	var counts admissionPostgresCounts
	err := q.QueryRow(context.Background(), `SELECT
 (SELECT count(*) FROM session_messages WHERE org_id=$1 AND session_id=$2),
 (SELECT count(*) FROM thread_inbox_entries WHERE org_id=$1 AND session_id=$2),
 (SELECT count(*) FROM session_input_dispatches WHERE org_id=$1 AND session_id=$2),
 (SELECT count(*) FROM jobs WHERE org_id=$1),
 (SELECT count(*) FROM session_threads WHERE org_id=$1 AND session_id=$2 AND admission_claim_dispatch_id IS NOT NULL),
 (SELECT COALESCE(sum(pending_message_count),0) FROM session_threads WHERE org_id=$1 AND session_id=$2)`, f.orgID, f.sessionID).
		Scan(&counts.messages, &counts.inbox, &counts.dispatches, &counts.jobs, &counts.claims, &counts.pending)
	require.NoError(t, err, "inspect the complete accepted-input graph only inside the synthetic tenant")
	return counts
}

func TestMessageAdmissionFullSchemaOutcomesPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		setup  func(*testing.T, *pgxpool.Pool, activatedLifecycleFixture)
		change func(*MessageAdmissionInput)
		want   MessageAdmissionDisposition
		reason string
		cause  error
	}{
		{name: "idle commits one new job", want: MessageAdmissionNewJob},
		{name: "ordinary binding does not borrow physical availability", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			admissionPostgresExec(t, p, `UPDATE sessions SET sandbox_state='destroyed',snapshot_key=NULL WHERE org_id=$1 AND id=$2`, f.orgID, f.sessionID)
		}, want: MessageAdmissionNewJob},
		{name: "agent tool is attribution not owner bypass", change: func(i *MessageAdmissionInput) { i.Source = models.SessionMessageSourceAgentTool }, want: MessageAdmissionNewJob},
		{name: "completed null end is resumable", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			admissionPostgresExec(t, p, `UPDATE session_threads SET status='completed' WHERE org_id=$1 AND id=$2`, f.orgID, f.threadID)
			admissionPostgresExec(t, p, `UPDATE sessions SET status='completed' WHERE org_id=$1 AND id=$2`, f.orgID, f.sessionID)
		}, want: MessageAdmissionNewJob},
		{name: "failed null end is resumable", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			admissionPostgresExec(t, p, `UPDATE session_threads SET status='failed' WHERE org_id=$1 AND id=$2`, f.orgID, f.threadID)
			admissionPostgresExec(t, p, `UPDATE sessions SET status='failed' WHERE org_id=$1 AND id=$2`, f.orgID, f.sessionID)
		}, want: MessageAdmissionNewJob},
		{name: "cancelled null end is resumable", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			admissionPostgresExec(t, p, `UPDATE session_threads SET status='cancelled' WHERE org_id=$1 AND id=$2`, f.orgID, f.threadID)
			admissionPostgresExec(t, p, `UPDATE sessions SET status='cancelled' WHERE org_id=$1 AND id=$2`, f.orgID, f.sessionID)
		}, want: MessageAdmissionNewJob},
		{name: "pending parent remains not resumable until start CAS is defined", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			admissionPostgresExec(t, p, `UPDATE sessions SET status='pending' WHERE org_id=$1 AND id=$2`, f.orgID, f.sessionID)
		}, cause: ErrMessageAdmissionNotResumable},
		{name: "skipped parent returns status conflict rather than actor refusal", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			admissionPostgresExec(t, p, `UPDATE sessions SET status='skipped' WHERE org_id=$1 AND id=$2`, f.orgID, f.sessionID)
		}, cause: ErrMessageAdmissionNotResumable},
		{name: "running queues without a second job", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			admissionPostgresExec(t, p, `UPDATE session_threads SET status='running' WHERE org_id=$1 AND id=$2`, f.orgID, f.threadID)
			admissionPostgresExec(t, p, `UPDATE sessions SET status='running' WHERE org_id=$1 AND id=$2`, f.orgID, f.sessionID)
		}, want: MessageAdmissionQueued},
		{name: "pending queues without a second job", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			admissionPostgresExec(t, p, `UPDATE session_threads SET status='pending' WHERE org_id=$1 AND id=$2`, f.orgID, f.threadID)
		}, want: MessageAdmissionQueued},
		{name: "session-wide legacy job waits", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			seedPredicateLegacyJob(t, p, lifecyclePredicateFixture{org: f.orgID, session: f.sessionID, lane: f.threadID}, fmt.Sprintf(`{"session_id":%q}`, f.sessionID))
		}, want: MessageAdmissionWaiting},
		{name: "workspace recovery waits despite expired schedule", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			seedLifecycleWorkspaceRecovery(t, p, f.orgID, f.sessionID)
		}, want: MessageAdmissionWaiting},
		{name: "unresolved whole-session physical operation waits", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			job := seedLifecycleConsumerJob(t, p, f.orgID, models.JobStatusFailed)
			seedLifecyclePreparation(t, p, f.orgID, f.sessionID, &f.primaryThreadID, job, "closed", "unknown")
		}, want: MessageAdmissionWaiting},
		{name: "raw orphan logical claim cannot be overwritten", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			dispatch, token := uuid.New(), uuid.New()
			admissionPostgresExec(t, p, `INSERT INTO session_input_dispatches(id,org_id,session_id,thread_id,kind,ingress_kind,ingress_key,payload_digest,validated_payload,normalization_version,admission_claim_token,admission_claim_turn) VALUES($1,$2,$3,$4,'message','synthetic_orphan',$1::uuid::text,repeat('a',64),'{}',0,$5,1)`, dispatch, f.orgID, f.sessionID, f.threadID, token)
			admissionPostgresExec(t, p, `UPDATE session_threads SET admission_claim_dispatch_id=$3,admission_claim_token=$4 WHERE org_id=$1 AND id=$2`, f.orgID, f.threadID, dispatch, token)
		}, want: MessageAdmissionWaiting},
		{name: "dangling raw automation marker refuses", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			admissionPostgresExec(t, p, `UPDATE sessions SET automation_owner_generation_id=$3 WHERE org_id=$1 AND id=$2`, f.orgID, f.sessionID, uuid.New())
		}, cause: ErrMessageAdmissionNotAllowed},
		{name: "specialist source cannot borrow ordinary authority", change: func(i *MessageAdmissionInput) { i.Source = models.SessionMessageSourceSystemAutoRepair }, cause: ErrMessageAdmissionNotAllowed},
		{name: "parent aggregate awaiting does not infer answer authority", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			admissionPostgresExec(t, p, `UPDATE sessions SET status='awaiting_input' WHERE org_id=$1 AND id=$2`, f.orgID, f.sessionID)
		}, want: MessageAdmissionNewJob},
		{name: "parent awaiting from explicit sibling request does not veto", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			request := seedMessageAdmissionPendingRequest(t, p, f, &f.primaryThreadID)
			admissionPostgresExec(t, p, `INSERT INTO session_input_workspace_compatibility(org_id,session_id,request_id,state) VALUES($1,$2,$3,'unprovable')`, f.orgID, f.sessionID, request)
			admissionPostgresExec(t, p, `UPDATE sessions SET status='awaiting_input' WHERE org_id=$1 AND id=$2`, f.orgID, f.sessionID)
		}, want: MessageAdmissionNewJob},
		{name: "pending target request cannot be answered by ordinary send", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			seedMessageAdmissionPendingRequest(t, p, f, &f.threadID)
		}, cause: ErrMessageAdmissionAwaitingInput},
		{name: "threadless pending request cannot be inferred as sibling", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			seedMessageAdmissionPendingRequest(t, p, f, nil)
		}, cause: ErrMessageAdmissionAwaitingInput},
		{name: "session-wide pending question requires A3", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			admissionPostgresExec(t, p, `INSERT INTO session_questions(org_id,session_id,question_text,status) VALUES($1,$2,'Synthetic session-wide question','pending')`, f.orgID, f.sessionID)
		}, cause: ErrMessageAdmissionAwaitingInput},
		{name: "contradictory request lane is not a compatible sibling", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			other := seedActivatedLifecycleFixture(t, context.Background(), p)
			seedMessageAdmissionPendingRequest(t, p, f, &other.threadID)
		}, cause: ErrMessageAdmissionAwaitingInput},
		{name: "active sibling repair waits for scope reconciliation", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			seedMessageAdmissionRepair(t, p, f, &f.primaryThreadID, true, "current-head")
		}, want: MessageAdmissionWaiting, reason: "repair_reconciliation"},
		{name: "released repair history alone is not a permanent owner", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			seedMessageAdmissionRepair(t, p, f, &f.primaryThreadID, false, "old-head")
		}, want: MessageAdmissionNewJob},
		{name: "comment command waits for repair only through whole rollback", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			seedMessageAdmissionRepair(t, p, f, &f.primaryThreadID, true, "current-head")
		}, change: func(i *MessageAdmissionInput) { i.ResolveReviewCommentIDs = []uuid.UUID{uuid.New()} }, cause: ErrMessageAdmissionBusy},
		{name: "awaiting-input lane never answers", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			admissionPostgresExec(t, p, `UPDATE session_threads SET status='awaiting_input' WHERE org_id=$1 AND id=$2`, f.orgID, f.threadID)
		}, cause: ErrMessageAdmissionAwaitingInput},
		{name: "explicit parent end refuses pending B3 join", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			admissionPostgresExec(t, p, `UPDATE sessions SET end_epoch=admission_epoch WHERE org_id=$1 AND id=$2`, f.orgID, f.sessionID)
		}, cause: ErrMessageAdmissionConflict},
		{name: "explicit lane end refuses pending B3 join", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			admissionPostgresExec(t, p, `UPDATE session_threads SET end_epoch=admission_epoch WHERE org_id=$1 AND id=$2`, f.orgID, f.threadID)
		}, cause: ErrMessageAdmissionConflict},
		{name: "stale original epoch refuses", setup: func(t *testing.T, p *pgxpool.Pool, f activatedLifecycleFixture) {
			admissionPostgresExec(t, p, `UPDATE session_threads SET admission_epoch=admission_epoch+1 WHERE org_id=$1 AND id=$2`, f.orgID, f.threadID)
		}, cause: ErrMessageAdmissionConflict},
		{name: "oversized message refuses", change: func(i *MessageAdmissionInput) { i.Message = strings.Repeat("x", (256<<10)+1) }, cause: ErrMessageAdmissionBackpressure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			f := seedActivatedLifecycleFixture(t, context.Background(), pool)
			command := admissionPostgresCommand(t, f, "synthetic-command", tt.change)
			if tt.setup != nil {
				tt.setup(t, pool, f)
			}
			before := admissionPostgresGraph(t, pool, f)
			a, effects, err := runAdmissionPostgres(t, pool, command)
			if tt.cause != nil {
				require.ErrorIs(t, err, tt.cause, "strict refusal must not become accepted waiting or latest-question answering")
				require.Equal(t, Admission{}, a, "failed admission must return no receipt")
				require.Empty(t, effects, "failed admission must expose no postcommit effects")
				require.Equal(t, before, admissionPostgresGraph(t, pool, f), "whole rollback must preserve every input job claim and pending count")
				return
			}
			require.NoError(t, err, "ordinary input must atomically commit its characterized outcome")
			require.Equal(t, tt.want, a.Disposition(), "accepted input must report its exact new queued or waiting disposition")
			if tt.reason != "" {
				require.Equal(t, tt.reason, a.Reason(), "uncertain repair ownership must remain explicit durable reconciliation rather than generic success or specialist refusal")
			}
			want := before
			want.messages++
			want.inbox++
			want.dispatches++
			if tt.want == MessageAdmissionNewJob {
				want.jobs++
				want.claims++
			} else {
				want.pending++
			}
			require.Equal(t, want, admissionPostgresGraph(t, pool, f), "exactly one message inbox dispatch and applicable job or pending count must commit together")
			require.NoError(t, a.validateLifecycleReceipt(), "only the canonical supplied unit may return a sealed receipt")
			require.NotEmpty(t, effects, "new acceptance must stage only data notifications for outer commit")
			var state, consumer string
			var due *time.Time
			var jobID *uuid.UUID
			require.NoError(t, pool.QueryRow(context.Background(), `SELECT admission_state,consumer_kind,next_attempt_at,job_id FROM session_input_dispatches WHERE org_id=$1 AND id=$2`, f.orgID, a.DispatchID()).Scan(&state, &consumer, &due, &jobID), "inspect the exact accepted dispatch without reading unrelated tenants")
			if tt.want == MessageAdmissionNewJob {
				require.Equal(t, []string{"bound", "job"}, []string{state, consumer}, "new job must own exactly one bound input")
				require.Nil(t, due, "bound input must not remain in the unbound waiting scan")
				require.NotNil(t, jobID, "bound input must name its new immutable job")
			} else {
				require.Equal(t, []string{"waiting", "none"}, []string{state, consumer}, "queued or blocked input must remain due and unbound")
				require.NotNil(t, due, "waiting acceptance must be independently discoverable")
				require.Nil(t, jobID, "waiting input cannot adopt any old or colliding job")
			}
		})
	}
}

func TestMessageAdmissionThreeSiblingJobsAndCertificatesPostgres(t *testing.T) {
	t.Parallel()
	// The installed representation has lane claim pairs only. Session-wide
	// dispatch/job/runtime/recovery blockers are exercised through canonical A2;
	// this is not proof of a deferred raw parent-claim representation or writer.
	pool := newLifecycleScopeSnapshotPool(t)
	f := seedActivatedLifecycleFixture(t, context.Background(), pool)
	lanes := []uuid.UUID{f.primaryThreadID, f.threadID, uuid.New(), uuid.New()}
	for _, id := range lanes[2:] {
		admissionPostgresExec(t, pool, `INSERT INTO session_threads(id,org_id,session_id,agent_type,label,status,lifecycle_version,admission_epoch) VALUES($1,$2,$3,$4,'Synthetic independent lane','idle',1,6)`, id, f.orgID, f.sessionID, models.DefaultDefaultAgentType)
	}
	for i, lane := range lanes {
		var epoch int64
		require.NoError(t, pool.QueryRow(context.Background(), `SELECT admission_epoch FROM session_threads WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.orgID, f.sessionID, lane).Scan(&epoch), "retain each explicit sibling epoch before any admission attempt")
		laneFixture := f
		laneFixture.threadID = lane
		laneFixture.fence = AdmissionFence{sessionID: f.sessionID, sessionEpoch: f.fence.sessionEpoch, threadID: lane, threadEpoch: epoch, threaded: true}
		command := admissionPostgresCommand(t, laneFixture, fmt.Sprintf("lane-%d", i), nil)
		a, _, err := runAdmissionPostgres(t, pool, command)
		require.NoError(t, err, "a sibling pending job or unprovable certificate cannot veto independent ordinary input")
		want := MessageAdmissionNewJob
		if i == 3 {
			want = MessageAdmissionWaiting
		}
		require.Equal(t, want, a.Disposition(), "three independently bound sibling jobs are supported and the fourth must wait durably")
		if i == 0 {
			request := seedMessageAdmissionPendingRequest(t, pool, f, &lane)
			admissionPostgresExec(t, pool, `INSERT INTO session_input_workspace_compatibility(org_id,session_id,request_id,state) VALUES($1,$2,$3,'unprovable')`, f.orgID, f.sessionID, request)
			admissionPostgresExec(t, pool, `UPDATE sessions SET status='awaiting_input' WHERE org_id=$1 AND id=$2`, f.orgID, f.sessionID)
		}
	}
	require.Equal(t, admissionPostgresCounts{messages: 4, inbox: 4, dispatches: 4, jobs: 3, claims: 3, pending: 1}, admissionPostgresGraph(t, pool, f), "only lane claims exist and no fourth consumer or lost input may be created")
}

func TestMessageAdmissionStaleRepairWorkPostgres(t *testing.T) {
	t.Parallel()
	// This is a required shared-consumer join, not an assertion that the original
	// active-reservation lookup proves released/old-head physical work absent.
	tests := []struct {
		name         string
		status       models.JobStatus
		targetStatus models.ThreadStatus
		runtime      bool
		commandOnly  bool
		capacity     bool
		comments     bool
		ordinary     bool
		want         MessageAdmissionDisposition
		reason       string
	}{
		{name: "released old-head pending sibling repair still waits", status: models.JobStatusPending, want: MessageAdmissionWaiting, reason: "repair_reconciliation"},
		{name: "released old-head running sibling repair still waits", status: models.JobStatusRunning, want: MessageAdmissionWaiting, reason: "repair_reconciliation"},
		{name: "released old-head parked sibling repair still waits", status: models.JobStatusAwaitingStartControl, want: MessageAdmissionWaiting, reason: "repair_reconciliation"},
		{name: "pending repair dominates running target queue", status: models.JobStatusPending, targetStatus: models.ThreadStatusRunning, want: MessageAdmissionWaiting, reason: "repair_reconciliation"},
		{name: "pending repair dominates pending target queue", status: models.JobStatusPending, targetStatus: models.ThreadStatusPending, want: MessageAdmissionWaiting, reason: "repair_reconciliation"},
		{name: "terminal repair job cannot erase retained sibling runtime", status: models.JobStatusFailed, runtime: true, want: MessageAdmissionWaiting, reason: "repair_reconciliation"},
		{name: "retained terminal repair dominates running target queue", status: models.JobStatusFailed, targetStatus: models.ThreadStatusRunning, runtime: true, want: MessageAdmissionWaiting, reason: "repair_reconciliation"},
		{name: "retained terminal repair dominates full sibling capacity", status: models.JobStatusFailed, runtime: true, capacity: true, want: MessageAdmissionWaiting, reason: "repair_reconciliation"},
		{name: "retained terminal repair comments roll back", status: models.JobStatusFailed, runtime: true, comments: true},
		{name: "retained terminal repair running comments roll back", status: models.JobStatusFailed, targetStatus: models.ThreadStatusRunning, runtime: true, comments: true},
		{name: "retained terminal repair capacity comments roll back", status: models.JobStatusFailed, runtime: true, capacity: true, comments: true},
		{name: "released terminal repair history alone permits ordinary job", status: models.JobStatusFailed, want: MessageAdmissionNewJob},
		{name: "released terminal repair history preserves running target queue", status: models.JobStatusFailed, targetStatus: models.ThreadStatusRunning, want: MessageAdmissionQueued, reason: "lane_running"},
		{name: "released terminal repair history preserves pending target queue", status: models.JobStatusFailed, targetStatus: models.ThreadStatusPending, want: MessageAdmissionQueued, reason: "lane_running"},
		{name: "pending repair command without reservation ID still waits", status: models.JobStatusPending, commandOnly: true, want: MessageAdmissionWaiting, reason: "repair_reconciliation"},
		{name: "terminal repair command without reservation ID retains live sibling runtime", status: models.JobStatusFailed, runtime: true, commandOnly: true, want: MessageAdmissionWaiting, reason: "repair_reconciliation"},
		{name: "retained terminal command dominates pending target queue", status: models.JobStatusFailed, targetStatus: models.ThreadStatusPending, runtime: true, commandOnly: true, want: MessageAdmissionWaiting, reason: "repair_reconciliation"},
		{name: "released terminal command history alone is not an owner", status: models.JobStatusFailed, commandOnly: true, want: MessageAdmissionNewJob},
		{name: "ordinary pending sibling preserves running target queue", status: models.JobStatusPending, targetStatus: models.ThreadStatusRunning, ordinary: true, want: MessageAdmissionQueued, reason: "lane_running"},
		{name: "ordinary pending sibling preserves pending target queue", status: models.JobStatusPending, targetStatus: models.ThreadStatusPending, ordinary: true, want: MessageAdmissionQueued, reason: "lane_running"},
		{name: "ordinary pending sibling preserves full capacity reason", status: models.JobStatusPending, capacity: true, ordinary: true, want: MessageAdmissionWaiting, reason: "capacity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			f := seedActivatedLifecycleFixture(t, context.Background(), pool)
			if tt.targetStatus != "" {
				admissionPostgresExec(t, pool, `UPDATE session_threads SET status=$3,current_turn=4 WHERE org_id=$1 AND id=$2`, f.orgID, f.threadID, tt.targetStatus)
				admissionPostgresExec(t, pool, `UPDATE sessions SET status='running' WHERE org_id=$1 AND id=$2`, f.orgID, f.sessionID)
			}
			if tt.capacity {
				// Reuse the explicit extra-lane representation instead of changing
				// fixture creation limits or borrowing a target-lane claim.
				admissionPostgresExec(t, pool, `UPDATE session_threads SET status='running' WHERE org_id=$1 AND id=$2`, f.orgID, f.primaryThreadID)
				for range 2 {
					admissionPostgresExec(t, pool, `INSERT INTO session_threads(id,org_id,session_id,agent_type,label,status,lifecycle_version,admission_epoch) VALUES($1,$2,$3,$4,'Synthetic occupied sibling','running',1,6)`, uuid.New(), f.orgID, f.sessionID, models.DefaultDefaultAgentType)
				}
				admissionPostgresExec(t, pool, `UPDATE sessions SET status='running' WHERE org_id=$1 AND id=$2`, f.orgID, f.sessionID)
			}
			runID, prID := seedMessageAdmissionRepair(t, pool, f, &f.primaryThreadID, false, "old-head")
			admissionPostgresExec(t, pool, `UPDATE pull_request_repair_runs SET obsoleted_by_version=2 WHERE org_id=$1 AND id=$2`, f.orgID, runID)
			payload := fmt.Sprintf(`{"org_id":%q,"session_id":%q,"thread_id":%q,"repair_run_id":%q,"pull_request_id":%q,"head_sha":"old-head"}`, f.orgID, f.sessionID, f.primaryThreadID, runID, prID)
			if tt.commandOnly {
				// The actual worker accepts this repair mode without a reservation
				// ID. Its command field must not become ordinary sibling evidence.
				payload = fmt.Sprintf(`{"org_id":%q,"session_id":%q,"thread_id":%q,"command_type":%q,"pull_request_id":%q,"head_sha":"old-head"}`, f.orgID, f.sessionID, f.primaryThreadID, models.PullRequestRepairActionTypeResolveConflicts, prID)
			} else if tt.ordinary {
				payload = fmt.Sprintf(`{"org_id":%q,"session_id":%q,"thread_id":%q}`, f.orgID, f.sessionID, f.primaryThreadID)
			}
			job := seedPredicateLegacyJob(t, pool, lifecyclePredicateFixture{org: f.orgID, session: f.sessionID, lane: f.threadID, sibling: f.primaryThreadID}, payload)
			admissionPostgresExec(t, pool, `UPDATE jobs SET status=$3 WHERE org_id=$1 AND id=$2`, f.orgID, job, tt.status)
			if tt.runtime {
				seedMessageAdmissionRetainedRepairRuntime(t, pool, f, job)
			}
			var commentID uuid.UUID
			if tt.comments {
				actor := seedMessageAdmissionActor(t, pool, f)
				comment := models.SessionReviewComment{OrgID: f.orgID, SessionID: f.sessionID, UserID: actor, FilePath: "synthetic.go", LineNumber: 1, DiffSide: "new", Body: "Synthetic unresolved repair comment", PassNumber: 1}
				require.NoError(t, NewSessionReviewCommentStore(pool).Create(context.Background(), &comment), "seed an exact-session unresolved comment before testing whole-abort repair behavior")
				commentID = comment.ID
			}
			before := admissionPostgresGraph(t, pool, f)
			command := admissionPostgresCommand(t, f, "ordinary-after-repair", func(input *MessageAdmissionInput) {
				if tt.comments {
					input.ResolveReviewCommentIDs = []uuid.UUID{commentID}
				}
			})
			a, effects, err := runAdmissionPostgres(t, pool, command)
			if tt.comments {
				require.ErrorIs(t, err, ErrMessageAdmissionBusy, "comments cannot resolve without an immediately admitted new job even when repair scope is uncertain")
				require.Equal(t, Admission{}, a, "repair-wait comment refusal must expose no partial accepted receipt")
				require.Empty(t, effects, "whole-abort comment handling cannot publish effects or notify jobs")
				require.Equal(t, before, admissionPostgresGraph(t, pool, f), "repair-wait comment refusal must leave every accepted-input and claim counter unchanged")
				var resolved bool
				require.NoError(t, pool.QueryRow(context.Background(), `SELECT resolved FROM session_review_comments WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.orgID, f.sessionID, commentID).Scan(&resolved), "read the exact original comment after the supplied transaction rolled back")
				require.False(t, resolved, "repair reconciliation must retain the original unresolved review comment")
				return
			}
			require.NoError(t, err, "uncertain repair scope must preserve ordinary input without borrowing repair authority")
			require.Equal(t, tt.want, a.Disposition(), "reservation inactivity head drift and obsolescence must not erase live repair work or retain released history as an owner")
			require.Equal(t, tt.reason, a.Reason(), "retained repair uncertainty must dominate queue and capacity reasons without making released history an owner")
			turn := 1
			if tt.targetStatus == models.ThreadStatusRunning || tt.targetStatus == models.ThreadStatusPending {
				turn = 6
			}
			require.Equal(t, turn, a.Message().TurnNumber, "repair waiting must preserve the original ordinary lane's admitted turn calculation")
			want := before
			want.messages++
			want.inbox++
			want.dispatches++
			if tt.want == MessageAdmissionNewJob {
				want.jobs++
				want.claims++
			} else {
				want.pending++
				_, hasJob := a.JobID()
				_, hasClaim := a.Authority()
				require.False(t, hasJob, "repair reconciliation must not schedule another input consumer")
				require.False(t, hasClaim, "repair reconciliation must not install or borrow a logical lane claim")
				var state, consumer, reason string
				var due *time.Time
				var boundJob *uuid.UUID
				require.NoError(t, pool.QueryRow(context.Background(), `SELECT admission_state,consumer_kind,reason,next_attempt_at,job_id FROM session_input_dispatches WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.orgID, f.sessionID, a.DispatchID()).Scan(&state, &consumer, &reason, &due, &boundJob), "inspect only the newly accepted exact-tenant dispatch")
				require.Equal(t, []string{"waiting", "none", tt.reason}, []string{state, consumer, reason}, "queued or repair/capacity waiting input must retain its exact durable unbound reason")
				require.NotNil(t, due, "accepted input without a job must remain independently due and discoverable")
				require.Nil(t, boundJob, "accepted waiting input cannot adopt the retained specialist or ordinary sibling job")
			}
			require.Equal(t, want, admissionPostgresGraph(t, pool, f), "repair uncertainty must commit only accepted input and never mutate stale specialist state")
			require.NotEmpty(t, effects, "new durable waiting input still needs ordinary postcommit data notifications")
		})
	}
}

func TestMessageAdmissionChangesetScopePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		foreign bool
		other   bool
	}{
		{"own unmaterialized changeset is association not availability", false, false},
		{"another session changeset is not ordinary lane scope", false, true},
		{"foreign tenant changeset never grants scoped access", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			f := seedActivatedLifecycleFixture(t, context.Background(), pool)
			orgID, sessionID := f.orgID, f.sessionID
			if tt.foreign {
				other := seedActivatedLifecycleFixture(t, context.Background(), pool)
				orgID, sessionID = other.orgID, other.sessionID
			} else if tt.other {
				session := models.Session{OrgID: f.orgID, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusIdle, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
				require.NoError(t, RunLifecycleTransaction(context.Background(), pool, func(ctx context.Context, tx pgx.Tx) error { return NewSessionStore(tx).CreateInTx(ctx, tx, &session) }), "same-tenant foreign session fixture must use the real supplied transaction store")
				sessionID = session.ID
			}
			changeset, holder := uuid.New(), uuid.New()
			admissionPostgresExec(t, pool, `INSERT INTO session_changesets(id,org_id,session_id,order_index,title,target_branch,base_branch,working_branch) VALUES($1,$2,$3,100,'Synthetic unmaterialized scope','main','main','synthetic-scope')`, changeset, orgID, sessionID)
			command := admissionPostgresCommand(t, f, "exact-changeset-scope", func(input *MessageAdmissionInput) { input.ChangesetID, input.LeaseHolderID = &changeset, &holder })
			before := admissionPostgresGraph(t, pool, f)
			a, effects, err := runAdmissionPostgres(t, pool, command)
			if tt.other {
				require.ErrorIs(t, err, pgx.ErrNoRows, "wrong session or tenant association must be hidden rather than treated as an availability wait")
				require.ErrorIs(t, err, ErrMessageAdmissionChangesetNotFound, "association-only changeset absence must retain its truthful target-not-found category")
				require.Equal(t, Admission{}, a, "foreign association cannot yield a sealed admission receipt")
				require.Empty(t, effects, "foreign association cannot yield postcommit effects")
				require.Equal(t, before, admissionPostgresGraph(t, pool, f), "association failure must leave accepted input and claims unchanged")
				return
			}
			require.NoError(t, err, "a valid unmaterialized changeset needs no positive physical permission at admission")
			require.Equal(t, MessageAdmissionNewJob, a.Disposition(), "valid scoped changeset can bind logical ordinary work before later physical start checks")
			retained, hasHolder := a.LeaseHolderID()
			require.True(t, hasHolder, "accepted envelope must retain original server changeset leaseholder")
			require.Equal(t, holder, retained, "server leaseholder must be retained exactly without being used as permission")
		})
	}
}

func TestMessageAdmissionReplayHistoricalOptionsAndCorruptionPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		mutation string
		cause    error
	}{
		{"new current epoch does not veto", "epoch", nil},
		{"current pending parent does not veto exact accepted receipt", "parent_pending", nil},
		{"changed server retry default retains original concrete options", "server_default", nil},
		{"new current end or awaiting status does not veto", "status", nil},
		{"current backoff and target do not replace original options", "schedule", nil},
		{"current retained owner does not veto accepted input", "owner", nil},
		{"new retained terminal repair cannot replace an exact historical receipt", "repair", nil},
		{"archived exact resource refuses disclosure", "hidden", pgx.ErrNoRows},
		{"mutated immutable job payload refuses", "payload", ErrLifecycleConsumerReconciliation},
		{"conflicting same-key semantic intent refuses", "digest", ErrMessageAdmissionConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			f := seedActivatedLifecycleFixture(t, context.Background(), pool)
			command := admissionPostgresCommand(t, f, "stable-replay", nil)
			a, _, err := runAdmissionPostgres(t, pool, command)
			require.NoError(t, err, "establish exactly one committed accepted receipt before changing current observations")
			job, _ := a.JobID()
			switch tt.mutation {
			case "epoch":
				admissionPostgresExec(t, pool, `UPDATE session_threads SET admission_epoch=admission_epoch+1 WHERE org_id=$1 AND id=$2`, f.orgID, f.threadID)
			case "parent_pending":
				admissionPostgresExec(t, pool, `UPDATE sessions SET status='pending' WHERE org_id=$1 AND id=$2`, f.orgID, f.sessionID)
			case "server_default":
				input := command.Input()
				input.Dispatch.MaxAttempts += 2
				input.Dispatch.RunAt = input.Dispatch.RunAt.Add(time.Hour)
				input.Ingress = prepareMessageAdmissionCommandFixture(t, input, "stable-replay")
				command, err = NewMessageAdmission(input)
				require.NoError(t, err, "an identical client retry must remain valid with independently chosen current server queue defaults")
			case "status":
				admissionPostgresExec(t, pool, `UPDATE session_threads SET status='awaiting_input',end_epoch=admission_epoch WHERE org_id=$1 AND id=$2`, f.orgID, f.threadID)
			case "schedule":
				admissionPostgresExec(t, pool, `UPDATE jobs SET run_at=run_at+interval '1 hour',target_node_id='synthetic-retarget' WHERE org_id=$1 AND id=$2`, f.orgID, job)
			case "owner":
				admissionPostgresExec(t, pool, `UPDATE sessions SET automation_owner_generation_id=$3 WHERE org_id=$1 AND id=$2`, f.orgID, f.sessionID, uuid.New())
			case "repair":
				runID, prID := seedMessageAdmissionRepair(t, pool, f, &f.primaryThreadID, false, "old-head")
				admissionPostgresExec(t, pool, `UPDATE pull_request_repair_runs SET obsoleted_by_version=2 WHERE org_id=$1 AND id=$2`, f.orgID, runID)
				payload := fmt.Sprintf(`{"org_id":%q,"session_id":%q,"thread_id":%q,"repair_run_id":%q,"pull_request_id":%q}`, f.orgID, f.sessionID, f.primaryThreadID, runID, prID)
				repairJob := seedPredicateLegacyJob(t, pool, lifecyclePredicateFixture{org: f.orgID, session: f.sessionID, lane: f.threadID, sibling: f.primaryThreadID}, payload)
				admissionPostgresExec(t, pool, `UPDATE jobs SET status='failed' WHERE org_id=$1 AND id=$2`, f.orgID, repairJob)
				seedMessageAdmissionRetainedRepairRuntime(t, pool, f, repairJob)
			case "hidden":
				admissionPostgresExec(t, pool, `UPDATE sessions SET archived_at=now() WHERE org_id=$1 AND id=$2`, f.orgID, f.sessionID)
			case "payload":
				admissionPostgresExec(t, pool, `UPDATE jobs SET payload=jsonb_set(payload,'{post_success_action}','"unreviewed_action"') WHERE org_id=$1 AND id=$2`, f.orgID, job)
			case "digest":
				input := command.Input()
				input.Message += " conflicting intent"
				input.Ingress = prepareMessageAdmissionCommandFixture(t, input, "stable-replay")
				command, err = NewMessageAdmission(input)
				require.NoError(t, err, "conflicting reuse fixture must itself be a shape-valid independently frozen command")
			}
			before := admissionPostgresGraph(t, pool, f)
			replay, effects, err := runAdmissionPostgres(t, pool, command)
			if tt.cause != nil {
				require.ErrorIs(t, err, tt.cause, "access or immutable identity corruption must fail closed even for a known key")
				require.Equal(t, Admission{}, replay, "refused receipt observation must not expose accepted data")
			} else {
				require.NoError(t, err, "exact committed receipt takes precedence over new admission eligibility")
				require.Equal(t, MessageAdmissionReplayed, replay.Disposition(), "a committed exact key must report replay not new work")
				require.Equal(t, a.DispatchID(), replay.DispatchID(), "replay cannot create or adopt a different dispatch")
				require.Equal(t, a.OriginalDisposition(), replay.OriginalDisposition(), "new repair uncertainty cannot replace the original committed admission outcome")
				require.Equal(t, a.Reason(), replay.Reason(), "historical replay must preserve its original reason rather than classify current repair work")
				require.Equal(t, a.DispatchOptions(), replay.DispatchOptions(), "historical replay must return original concrete scheduling options")
				require.Equal(t, a.TargetNodeID(), replay.TargetNodeID(), "current retargeting must not replace retained original routing choices")
			}
			require.Empty(t, effects, "historical replay or refusal can never reissue postcommit effects")
			require.Equal(t, before, admissionPostgresGraph(t, pool, f), "replay and conflicting reuse must leave the complete accepted graph unchanged")
		})
	}
}

func TestMessageAdmissionReplayAfterContinuationAttemptPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		mutation string
	}{
		{name: "actual lease-fenced continuation progress preserves exact replay"},
		{name: "changed post-success action still refuses", mutation: "action"},
		{name: "foreign tenant payload still refuses", mutation: "org"},
		{name: "foreign session payload still refuses", mutation: "session"},
		{name: "foreign lane payload still refuses", mutation: "lane"},
		{name: "changed input binding still refuses", mutation: "binding"},
		{name: "changed queued instruction still refuses", mutation: "message"},
		{name: "unrecognized payload addition still refuses", mutation: "unexpected"},
		{name: "changed queue still refuses", mutation: "queue"},
		{name: "changed job type still refuses", mutation: "type"},
		{name: "changed persisted retry budget still refuses", mutation: "job_budget"},
		{name: "changed retained retry budget still refuses durable binding", mutation: "envelope_budget"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// The actual claimant scans its queue across tenants. Give each case
			// an isolated database so another parallel case cannot steal its job.
			pool := newLifecycleScopeSnapshotPool(t)
			ctx := context.Background()
			f := seedActivatedLifecycleFixture(t, ctx, pool)
			command := admissionPostgresCommand(t, f, "replay-after-actual-start", func(input *MessageAdmissionInput) {
				// Use explicit immutable collections so the complete message
				// comparison does not conflate nil construction values with the
				// store's canonical empty JSON collections on readback.
				input.References = models.SessionInputReferences{{Kind: models.SessionInputReferenceKindFile, Path: "synthetic.go", Display: "synthetic.go"}}
				input.Commands = models.SessionInputCommands{{Kind: "command", AgentType: models.DefaultDefaultAgentType, Name: "test", Token: "/test", Display: "/test", Source: models.SessionInputCommandSourceBuiltin}}
			})
			accepted, _, err := runAdmissionPostgres(t, pool, command)
			require.NoError(t, err, "commit the canonical bound job before its actual current worker starts")
			jobID, hasJob := accepted.JobID()
			require.True(t, hasJob, "idle admission must bind exactly one immutable input job")
			node, token := "synthetic-replay-worker-"+uuid.NewString(), uuid.New()
			admissionPostgresExec(t, pool, `INSERT INTO nodes(id,mode,status,last_heartbeat_at) VALUES($1,'worker','active',now())`, node)
			claimed, err := NewJobStore(pool).ClaimNextRunnable(ctx, node, "synthetic-replay-owner", token, time.Minute)
			require.NoError(t, err, "the real queue claimant must establish the job's running lease")
			require.NotNil(t, claimed, "the isolated queue must have its newly admitted due job")
			require.Equal(t, jobID, claimed.ID, "the real claimant must own the exact admitted job, not another queue item")
			runtime := createSeedRuntime(t, pool, f.orgID, f.sessionID, f.threadID)
			store := NewJobStore(pool)
			changed, err := store.RecordContinuationAttemptWithLease(ctx, f.orgID, jobID, uuid.New(), f.sessionID, f.threadID, runtime.ID, runtime.LeaseToken, accepted.Message().ID, []int64{accepted.Message().ID})
			require.NoError(t, err, "a stale job token must be a clean fenced refusal")
			require.False(t, changed, "no attempt may be recorded without the actual job lease token")
			changed, err = store.RecordContinuationAttemptWithLease(ctx, f.orgID, jobID, token, f.sessionID, f.threadID, runtime.ID, uuid.New(), accepted.Message().ID, []int64{accepted.Message().ID})
			require.NoError(t, err, "a stale runtime token must be a clean fenced refusal")
			require.False(t, changed, "the current job lease alone cannot record a foreign runtime attempt")
			changed, err = store.RecordContinuationAttemptWithLease(ctx, f.orgID, jobID, token, f.sessionID, f.threadID, runtime.ID, runtime.LeaseToken, accepted.Message().ID, []int64{accepted.Message().ID})
			require.NoError(t, err, "the actual continuation-attempt writer must accept both current lease generations")
			require.True(t, changed, "the real start writer must actually rewrite the persisted payload before replay is tested")
			var progress struct {
				Runtime string  `json:"continuation_runtime_id"`
				Message string  `json:"continuation_message_id"`
				IDs     []int64 `json:"continuation_task_message_ids"`
			}
			var payload []byte
			require.NoError(t, pool.QueryRow(ctx, `SELECT payload FROM jobs WHERE org_id=$1 AND id=$2`, f.orgID, jobID).Scan(&payload), "read the exact payload after the real fenced attempt writer")
			require.NoError(t, json.Unmarshal(payload, &progress), "decode only the three documented mutable attempt-progress fields")
			require.Equal(t, runtime.ID.String(), progress.Runtime, "the actual writer must retain the current exact runtime identity")
			require.Equal(t, fmt.Sprint(accepted.Message().ID), progress.Message, "the current attempt must retain the original admitted instruction")
			require.Equal(t, []int64{accepted.Message().ID}, progress.IDs, "the current attempt must retain exactly its original task message set")
			switch tt.mutation {
			case "action":
				admissionPostgresExec(t, pool, `UPDATE jobs SET payload=jsonb_set(payload,'{post_success_action}','"unreviewed_action"') WHERE org_id=$1 AND id=$2`, f.orgID, jobID)
			case "org", "session", "lane":
				key := map[string]string{"org": "org_id", "session": "session_id", "lane": "thread_id"}[tt.mutation]
				admissionPostgresExec(t, pool, `UPDATE jobs SET payload=jsonb_set(payload,ARRAY[$3::text],to_jsonb($4::text)) WHERE org_id=$1 AND id=$2`, f.orgID, jobID, key, uuid.NewString())
			case "binding":
				admissionPostgresExec(t, pool, `UPDATE jobs SET payload=jsonb_set(payload,'{input_dispatch_ids}',jsonb_build_array(gen_random_uuid()::text)) WHERE org_id=$1 AND id=$2`, f.orgID, jobID)
			case "message":
				admissionPostgresExec(t, pool, `UPDATE jobs SET payload=jsonb_set(payload,'{queued_message_id}','"999999999"') WHERE org_id=$1 AND id=$2`, f.orgID, jobID)
			case "unexpected":
				admissionPostgresExec(t, pool, `UPDATE jobs SET payload=payload||'{"unrecognized_progress":true}' WHERE org_id=$1 AND id=$2`, f.orgID, jobID)
			case "queue":
				admissionPostgresExec(t, pool, `UPDATE jobs SET queue='synthetic_other' WHERE org_id=$1 AND id=$2`, f.orgID, jobID)
			case "type":
				admissionPostgresExec(t, pool, `UPDATE jobs SET job_type='run_agent' WHERE org_id=$1 AND id=$2`, f.orgID, jobID)
			case "job_budget":
				admissionPostgresExec(t, pool, `UPDATE jobs SET max_attempts=max_attempts+1 WHERE org_id=$1 AND id=$2`, f.orgID, jobID)
			case "envelope_budget":
				admissionPostgresExec(t, pool, `UPDATE session_input_dispatches SET validated_payload=jsonb_set(validated_payload,'{max_attempts}',to_jsonb($3::integer)) WHERE org_id=$1 AND id=$2`, f.orgID, accepted.DispatchID(), accepted.DispatchOptions().MaxAttempts+1)
			}
			before := admissionPostgresGraph(t, pool, f)
			replayed, effects, err := runAdmissionPostgres(t, pool, command)
			if tt.mutation != "" {
				require.ErrorIs(t, err, ErrLifecycleConsumerReconciliation, "permitting documented attempt progress must not permit corruption of any immutable admitted binding")
				require.Equal(t, Admission{}, replayed, "corrupted immutable binding must expose no partial receipt")
			} else {
				require.NoError(t, err, "an identical client retry must survive the existing real start writer")
				require.Equal(t, MessageAdmissionReplayed, replayed.Disposition(), "started accepted work must return its original receipt rather than create work")
				require.Equal(t, accepted.OriginalDisposition(), replayed.OriginalDisposition(), "worker progress cannot change the accepted outcome")
				require.Equal(t, accepted.DispatchID(), replayed.DispatchID(), "replay must retain the exact accepted dispatch")
				require.Equal(t, accepted.DispatchOptions(), replayed.DispatchOptions(), "replay must retain original concrete server options")
				require.Equal(t, accepted.Message(), replayed.Message(), "replay must return the exact original admitted message")
			}
			require.Empty(t, effects, "replay or corruption refusal cannot repeat any postcommit effect")
			require.Equal(t, before, admissionPostgresGraph(t, pool, f), "replay and refusal must leave the entire accepted graph unchanged")
		})
	}
}

type messageAdmissionFaultTx struct {
	pgx.Tx
	cause error
}

func (t messageAdmissionFaultTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(query, "UPDATE session_input_dispatches SET validated_payload=") {
		return pgconn.CommandTag{}, t.cause
	}
	return t.Tx.Exec(ctx, query, args...)
}

func TestMessageAdmissionLateFailureWholeRollbackPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	f := seedActivatedLifecycleFixture(t, context.Background(), pool)
	command := admissionPostgresCommand(t, f, "late-failure", nil)
	before := admissionPostgresGraph(t, pool, f)
	ctx, cancel, owner := beginLifecycleLaneClaimOwner(t, context.Background(), pool)
	defer cancel()
	cause := errors.New("synthetic late persistence failure")
	result, err := AdmitMessageInTx(ctx, messageAdmissionFaultTx{Tx: owner, cause: cause}, f.orgID, command)
	require.ErrorIs(t, err, cause, "late error after all provisional input job and claim writes must reach the outer transaction owner")
	_, _, unpackErr := result.Unpack()
	require.Error(t, unpackErr, "late failure must expose no accepted receipt or effects")
	require.Equal(t, admissionPostgresCounts{messages: 1, inbox: 1, dispatches: 1, jobs: 1, claims: 1}, admissionPostgresGraph(t, owner, f), "the synthetic late failure must really occur after the entire provisional graph exists")
	require.NoError(t, owner.Rollback(ctx), "the supplied owner must confirm complete rollback rather than compensating selected rows")
	require.Equal(t, before, admissionPostgresGraph(t, pool, f), "rollback must remove every provisional message inbox dispatch job claim and parent projection")
}

func TestMessageAdmissionConcurrentExactKeyPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	f := seedActivatedLifecycleFixture(t, context.Background(), pool)
	command := admissionPostgresCommand(t, f, "concurrent-same-key", nil)
	start := make(chan struct{})
	type outcome struct {
		a       Admission
		effects []LifecycleEffect
		err     error
	}
	results := make(chan outcome, 2)
	var group sync.WaitGroup
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			a, effects, err := runAdmissionPostgres(t, pool, command)
			results <- outcome{a, effects, err}
		}()
	}
	close(start)
	group.Wait()
	close(results)
	newCount, replayCount := 0, 0
	for result := range results {
		if errors.Is(result.err, ErrMessageAdmissionReplayObservationNeeded) {
			// Run returned only after known complete rollback. Observe the SAME
			// ingress; never recapture epochs or blindly repeat the mutation.
			a, found, err := observeAdmissionPostgres(t, pool, command)
			require.NoError(t, err, "confirmed-aborted concurrent loser must resolve one fresh exact receipt observation")
			require.True(t, found, "the committed winner must be positively observed before exposing replay")
			result.a, result.err = a, err
		}
		require.NoError(t, result.err, "one exact-key winner and one unchanged replay must resolve without duplicating input")
		if result.a.Disposition() == MessageAdmissionNewJob {
			newCount++
			require.NotEmpty(t, result.effects, "only the true committed winner may expose staged notifications")
		} else {
			replayCount++
			require.Equal(t, MessageAdmissionReplayed, result.a.Disposition(), "same-payload loser must be exact receipt replay")
			require.Empty(t, result.effects, "replay must never duplicate winner notifications")
		}
	}
	require.Equal(t, []int{1, 1}, []int{newCount, replayCount}, "two same-key callers must produce exactly one durable new admission and one replay")
	require.Equal(t, admissionPostgresCounts{messages: 1, inbox: 1, dispatches: 1, jobs: 1, claims: 1}, admissionPostgresGraph(t, pool, f), "concurrent exact-key reuse must create exactly one complete accepted graph")
}

func seedMessageAdmissionActor(t *testing.T, pool *pgxpool.Pool, f activatedLifecycleFixture) uuid.UUID {
	t.Helper()
	admin := uuid.New()
	admissionPostgresExec(t, pool, `INSERT INTO users(id,org_id,email,name,role) VALUES($1,$2,$3,'Synthetic admission administrator','admin')`, admin, f.orgID, admin.String()+"@example.invalid")
	admissionPostgresExec(t, pool, `INSERT INTO organization_memberships(user_id,org_id,role) VALUES($1,$2,'admin')`, admin, f.orgID)
	id := uuid.New()
	admissionPostgresExec(t, pool, `INSERT INTO users(id,org_id,email,name,role) VALUES($1,$2,$3,'Synthetic admission actor','member')`, id, f.orgID, id.String()+"@example.invalid")
	admissionPostgresExec(t, pool, `INSERT INTO organization_memberships(user_id,org_id,role) VALUES($1,$2,'member') ON CONFLICT(user_id,org_id) DO UPDATE SET role='member'`, id, f.orgID)
	return id
}

func TestMessageAdmissionEnqueueCollisionAndCommentRollbackPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		comments bool
	}{{"ordinary input stays accepted waiting", false}, {"comment command rolls all provisional writes back", true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			f := seedActivatedLifecycleFixture(t, context.Background(), pool)
			foreign := seedActivatedLifecycleFixture(t, context.Background(), pool)
			dedupe := "synthetic-global-collision:" + uuid.NewString()
			admissionPostgresExec(t, pool, `INSERT INTO jobs(org_id,queue,job_type,payload,status,dedupe_key) VALUES($1,'agent','continue_session','{}','pending',$2)`, foreign.orgID, dedupe)
			var commentID uuid.UUID
			if tt.comments {
				actor := seedMessageAdmissionActor(t, pool, f)
				comment := models.SessionReviewComment{OrgID: f.orgID, SessionID: f.sessionID, UserID: actor, FilePath: "synthetic.go", LineNumber: 1, DiffSide: "new", Body: "Synthetic unresolved comment", PassNumber: 1}
				require.NoError(t, NewSessionReviewCommentStore(pool).Create(context.Background(), &comment), "seed an isolated exact-session unresolved review comment")
				commentID = comment.ID
			}
			command := admissionPostgresCommand(t, f, "collision-command", func(i *MessageAdmissionInput) {
				i.ContinuationDedupeKeyOverride = &dedupe
				if tt.comments {
					i.ResolveReviewCommentIDs = []uuid.UUID{commentID}
				}
			})
			before := admissionPostgresGraph(t, pool, f)
			ctx, cancel, owner := beginLifecycleLaneClaimOwner(t, context.Background(), pool)
			defer cancel()
			result, err := AdmitMessageInTx(ctx, owner, f.orgID, command)
			if tt.comments {
				require.ErrorIs(t, err, ErrMessageAdmissionBusy, "comment adapter busy fallback must abort even when collision is discovered after provisional comment resolution")
				var resolved bool
				require.NoError(t, owner.QueryRow(ctx, `SELECT resolved FROM session_review_comments WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.orgID, f.sessionID, commentID).Scan(&resolved), "inspect the real provisional comment inside the still-owned failed unit")
				require.True(t, resolved, "this collision must occur after actual comment resolution rather than only preflight refusal")
				want := before
				want.messages++
				want.inbox++
				want.dispatches++
				require.Equal(t, want, admissionPostgresGraph(t, owner, f), "collision must have staged input but no job claim slot projection or pending counter")
				_, _, unpackErr := result.Unpack()
				require.Error(t, unpackErr, "busy collision must expose no accepted receipt or notifications")
				require.NoError(t, owner.Rollback(ctx), "outer owner must roll the entire provisional comment input graph back")
				require.Equal(t, before, admissionPostgresGraph(t, pool, f), "no message inbox dispatch job claim or counter may survive a busy comment command")
				require.NoError(t, pool.QueryRow(context.Background(), `SELECT resolved FROM session_review_comments WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.orgID, f.sessionID, commentID).Scan(&resolved), "read the original comment independently after confirmed rollback")
				require.False(t, resolved, "comment resolution must roll back with the unsent busy input")
			} else {
				require.NoError(t, err, "ordinary enqueue collision must preserve accepted input as durable waiting")
				require.NoError(t, owner.Commit(ctx), "only the supplied owner may commit the waiting receipt")
				a, effects, err := result.Unpack()
				require.NoError(t, err, "known committed waiting receipt must unpack normally")
				require.Equal(t, MessageAdmissionWaiting, a.Disposition(), "global dedupe collision cannot be mislabeled a bound existing consumer")
				require.Equal(t, "enqueue_reconciliation", a.Reason(), "accepted waiting input must retain a discoverable explicit collision reason")
				_, hasJob := a.JobID()
				require.False(t, hasJob, "waiting collision must not adopt the foreign colliding job")
				_, hasClaim := a.Authority()
				require.False(t, hasClaim, "waiting collision must not manufacture an admission claim")
				want := before
				want.messages++
				want.inbox++
				want.dispatches++
				want.pending++
				require.Equal(t, want, admissionPostgresGraph(t, pool, f), "ordinary collision must keep only the complete waiting input graph")
				for _, effect := range effects {
					require.NotEqual(t, LifecycleEffectWakeJobs, effect.Kind(), "unbound collision must not notify a nonexistent or foreign job")
				}
			}
		})
	}
}

func TestMessageAdmissionReplayActorMembershipPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	f := seedActivatedLifecycleFixture(t, context.Background(), pool)
	actor := seedMessageAdmissionActor(t, pool, f)
	command := admissionPostgresCommand(t, f, "actor-replay", func(i *MessageAdmissionInput) { i.ActorID = &actor })
	a, _, err := runAdmissionPostgres(t, pool, command)
	require.NoError(t, err, "current write-capable tenant membership must authorize the original ordinary command")
	admissionPostgresExec(t, pool, `DELETE FROM organization_memberships WHERE org_id=$1 AND user_id=$2`, f.orgID, actor)
	before := admissionPostgresGraph(t, pool, f)
	replay, found, err := observeAdmissionPostgres(t, pool, command)
	require.ErrorIs(t, err, ErrMessageAdmissionNotAllowed, "digest-bound historical actor attribution must not replace current tenant membership for receipt disclosure")
	require.False(t, found, "membership refusal must not expose a positive replay receipt")
	require.Equal(t, Admission{}, replay, "membership refusal must not expose retained message contents")
	require.Equal(t, before, admissionPostgresGraph(t, pool, f), "revoked access must not destroy or reissue the original accepted input")
	require.NotEqual(t, uuid.Nil, a.DispatchID(), "original accepted receipt remains durable despite later access refusal")
}

func TestMessageAdmissionExactReceiptAfterLostCommitResponsePostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	f := seedActivatedLifecycleFixture(t, context.Background(), pool)
	changeset, err := NewSessionStore(pool).GetPrimaryChangesetID(context.Background(), f.orgID, f.sessionID)
	require.NoError(t, err, "use the real fixture's tenant-scoped primary changeset")
	holder := uuid.New()
	command := admissionPostgresCommand(t, f, "simulated-lost-response", func(i *MessageAdmissionInput) { i.ChangesetID = &changeset; i.LeaseHolderID = &holder })
	ctx, cancel, owner := beginLifecycleLaneClaimOwner(t, context.Background(), pool)
	defer cancel()
	_, err = AdmitMessageInTx(ctx, owner, f.orgID, command)
	require.NoError(t, err, "stage the actual atomic admission without using its receipt as proof of commit")
	require.NoError(t, owner.Commit(ctx), "commit the real transaction before simulating loss of the caller's commit response")
	// This deliberately discards the staged return value. It is a positive
	// receipt-recovery test, NOT a claim to simulate a network or commit error.
	fresh := command.Input()
	fresh.Dispatch.RunAt = fresh.Dispatch.RunAt.Add(time.Hour)
	freshHolder := uuid.New()
	fresh.LeaseHolderID = &freshHolder
	freshCommand, err := NewMessageAdmission(fresh)
	require.NoError(t, err, "excluded fresh server choices cannot change the stable client semantic ingress")
	before := admissionPostgresGraph(t, pool, f)
	a, found, err := observeAdmissionPostgres(t, pool, freshCommand)
	require.NoError(t, err, "only a fresh exact durable receipt observation may recover the accepted outcome")
	require.True(t, found, "positive exact receipt must be observed before reporting accepted input")
	require.Equal(t, MessageAdmissionReplayed, a.Disposition(), "lost response recovery must not repeat admission")
	require.Equal(t, command.Dispatch(), a.DispatchOptions(), "receipt lookup must retain original concrete options rather than fresh retry choices")
	retainedHolder, hasHolder := a.LeaseHolderID()
	require.True(t, hasHolder, "the original changeset holder must remain part of the durable accepted envelope")
	require.Equal(t, holder, retainedHolder, "a retry cannot replace the original generated changeset holder")
	job, hasJob := a.JobID()
	require.True(t, hasJob, "exact accepted input must retain its original immutable job identity")
	var payloadHolder string
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT payload->>'changeset_lease_holder_id' FROM jobs WHERE org_id=$1 AND id=$2`, f.orgID, job).Scan(&payloadHolder), "read only the exact retained job's server holder identity")
	require.Equal(t, holder.String(), payloadHolder, "durable job payload must retain the same original holder as the accepted receipt")
	require.Equal(t, before, admissionPostgresGraph(t, pool, f), "positive outcome recovery must not insert input jobs claims or duplicate effects")
}

type messageAdmissionCaptureOrderTx struct {
	pgx.Tx
	beforeParent func()
	observed     bool
}

func (t *messageAdmissionCaptureOrderTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	if query == lifecycleSessionScopeLockQuery && !t.observed {
		t.observed = true
		t.beforeParent()
	}
	return t.Tx.QueryRow(ctx, query, args...)
}

func TestMessageAdmissionCaptureKnownReceiptJobBeforeParentPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	f := seedActivatedLifecycleFixture(t, context.Background(), pool)
	command := admissionPostgresCommand(t, f, "known-capture-blocker", nil)
	a, _, err := runAdmissionPostgres(t, pool, command)
	require.NoError(t, err, "establish a real retained lane claim and immutable job before a pure new-command observation")
	job, _ := a.JobID()
	before := admissionPostgresGraph(t, pool, f)
	ctx, cancel, owner := beginLifecycleLaneClaimOwner(t, context.Background(), pool)
	defer cancel()
	wrapper := &messageAdmissionCaptureOrderTx{Tx: owner}
	wrapper.beforeParent = func() {
		for _, entry := range []struct {
			query string
			id    uuid.UUID
		}{
			{`SELECT id FROM session_input_dispatches WHERE org_id=$1 AND id=$2 FOR NO KEY UPDATE NOWAIT`, a.DispatchID()},
			{`SELECT id FROM jobs WHERE org_id=$1 AND id=$2 FOR NO KEY UPDATE NOWAIT`, job},
		} {
			probeCtx, probeCancel, probe := beginLifecycleLaneClaimOwner(t, ctx, pool)
			var id uuid.UUID
			err := probe.QueryRow(probeCtx, entry.query, f.orgID, entry.id).Scan(&id)
			requireSessionLifecycleSQLState(t, err, "55P03", "independent NOWAIT probe must prove the known receipt and existing job are already locked before parent acquisition")
			require.NoError(t, probe.Rollback(probeCtx), "the independent failed lock probe must release its own aborted unit")
			probeCancel()
		}
		probeLifecycleScopeRowLock(t, ctx, pool, f.orgID, f.sessionID, nil, false)
	}
	fence, err := CaptureOrdinaryMessageFenceInTx(ctx, wrapper, f.orgID, f.sessionID, f.threadID)
	require.NoError(t, err, "pure capture may observe the retained current claim without borrowing its admission or execution authority")
	require.Equal(t, f.fence, fence, "known-owner capture must return only the exact original current epochs")
	require.True(t, wrapper.observed, "physical lock-order probe must run before the actual parent lock query")
	require.NoError(t, owner.Commit(ctx), "only the observation owner may settle its lock-only transaction")
	require.Equal(t, before, admissionPostgresGraph(t, pool, f), "committing pure capture must not create input claims jobs counters or effects")
}
