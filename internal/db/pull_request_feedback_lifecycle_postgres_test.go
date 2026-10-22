package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func newFeedbackLifecyclePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return testpostgres.NewPool(t, context.Background(), "../../migrations", 0)
}

func seedFeedbackLifecycle(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	org, pr := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name,settings) VALUES($1,$2,'{}')`, org, "feedback-test-"+org.String())
	require.NoError(t, err, "seed independent feedback tenant")
	session := &models.Session{OrgID: org, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusIdle, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
	require.NoError(t, NewSessionStore(pool).Create(ctx, session), "create session with durable primary thread")
	_, err = pool.Exec(ctx, `INSERT INTO pull_requests(id,org_id,session_id,github_pr_number,github_pr_url,github_repo,title,status,head_sha) VALUES($1,$2,$3,9,'https://example.test/pull/9','example/app','Test feedback','open','head-before')`, pr, org, session.ID)
	require.NoError(t, err, "create owned open pull request")
	return org, pr, session.ID
}

func TestPRFeedbackCollectionLifecyclePostgres(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	tests := []struct {
		name, stage string
		author      models.PRFeedbackAuthorType
		wantStatus  models.PRFeedbackBatchStatus
		wantCode    string
		wantSource  models.PRFeedbackBatchSourceKind
	}{
		{"human resets bot budget", "", models.PRFeedbackAuthorTypeUser, models.PRFeedbackBatchStatusQueued, "", models.PRFeedbackBatchSourceHumanOrMixed},
		{"eligible bot consumes one cycle", "", models.PRFeedbackAuthorTypeBot, models.PRFeedbackBatchStatusQueued, "", models.PRFeedbackBatchSourceBotOnly},
		{"empty collection needs attention", "empty", models.PRFeedbackAuthorTypeUser, models.PRFeedbackBatchStatusNeedsAttention, "no_eligible_items", models.PRFeedbackBatchSourceHumanOrMixed},
		{"debounce does not claim early", "debounce", models.PRFeedbackAuthorTypeUser, models.PRFeedbackBatchStatusCollecting, "", models.PRFeedbackBatchSourceHumanOrMixed},
		{"bot cycle cap stops automatic work", "bot-cap", models.PRFeedbackAuthorTypeBot, models.PRFeedbackBatchStatusNeedsAttention, "bot_cycle_limit_exhausted", models.PRFeedbackBatchSourceHumanOrMixed},
		{"hourly cap stops automatic work", "hourly", models.PRFeedbackAuthorTypeUser, models.PRFeedbackBatchStatusNeedsAttention, "hourly_cap_exhausted", models.PRFeedbackBatchSourceHumanOrMixed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			org, pr, _ := seedFeedbackLifecycle(t, pool)
			store := NewPullRequestFeedbackStore(pool)
			_, err := pool.Exec(ctx, `UPDATE pull_requests SET feedback_bot_epoch=3,feedback_bot_cycles_in_epoch=2 WHERE org_id=$1 AND id=$2`, org, pr)
			require.NoError(t, err, "seed prior bot feedback budget")
			if tt.stage == "hourly" {
				_, err = pool.Exec(ctx, `INSERT INTO pull_request_feedback_items(org_id,pull_request_id,surface,provider_object_id,author_login,author_type,body,body_hash,status) SELECT $1,$2,'issue_comment',n,'tester','User','old','old','ignored' FROM generate_series(1,31) n`, org, pr)
				require.NoError(t, err, "seed actual recent provider item cap")
			}
			var item models.PullRequestFeedbackItem
			if tt.stage != "empty" && tt.stage != "hourly" {
				item = models.PullRequestFeedbackItem{PullRequestID: pr, Surface: models.PRFeedbackSurfaceIssueComment, ProviderObjectID: 8, AuthorLogin: "tester", AuthorType: tt.author, Body: "Please fix this", BodyHash: "hash", ObservedHeadSHA: "head-before", Intent: models.PRFeedbackIntentChangeRequest, Status: models.PRFeedbackItemStatusPending}
				require.NoError(t, store.UpsertItem(ctx, org, &item), "ingest eligible pending provider item")
			}
			now := time.Now().UTC()
			batch, created, err := store.EnsureCollectingBatch(ctx, org, pr, now)
			require.NoError(t, err, "create collecting batch under PR lock")
			require.True(t, created, "first collection must create one batch")
			same, created, err := store.EnsureCollectingBatch(ctx, org, pr, now)
			require.NoError(t, err, "repeated collection must reuse active batch")
			require.False(t, created, "duplicate trigger must not allocate another writer")
			require.Equal(t, batch, same, "duplicate trigger must preserve every batch field")
			when := now.Add(time.Minute)
			if tt.stage == "debounce" {
				when = now
			}
			limit := 4
			if tt.stage == "bot-cap" {
				limit = 2
			}
			actual, err := store.FinalizeCollectingBatch(ctx, org, batch.ID, when, &limit)
			require.NoError(t, err, "finalize collection using actual tenant and budget locks")
			require.Equal(t, tt.wantStatus, actual.Status, "collection must queue eligible work or persist exact attention state")
			var expectedCode *string
			if tt.wantCode != "" {
				expectedCode = &tt.wantCode
			}
			require.Equal(t, expectedCode, actual.ErrorCode, "attention state must identify exact exceeded cap or empty collection")
			require.Equal(t, tt.wantSource, actual.SourceKind, "collection must classify human and bot sources before changing budgets")
			persisted, err := store.GetBatch(ctx, org, batch.ID)
			require.NoError(t, err, "reload finalized batch")
			require.Equal(t, actual, persisted, "returned finalized batch must match durable state")
			if tt.wantStatus == models.PRFeedbackBatchStatusQueued {
				var snapshot []models.PullRequestFeedbackItem
				require.NoError(t, json.Unmarshal(actual.FeedbackSnapshot, &snapshot), "batch snapshot must contain immutable collected provider items")
				// JSON timestamps retain the instant and offset, not pgx's Local
				// location identity. Compare both representations in UTC.
				item.ReceivedAt = item.ReceivedAt.UTC()
				item.UpdatedAt = item.UpdatedAt.UTC()
				for i := range snapshot {
					snapshot[i].ReceivedAt = snapshot[i].ReceivedAt.UTC()
					snapshot[i].UpdatedAt = snapshot[i].UpdatedAt.UTC()
				}
				require.Equal(t, []models.PullRequestFeedbackItem{item}, snapshot, "snapshot must retain exact provider payload before claim")
				items, err := store.ListBatchItems(ctx, org, batch.ID)
				require.NoError(t, err, "load claimed items")
				require.Len(t, items, 1, "batch must claim the one eligible item")
				require.Equal(t, models.PRFeedbackItemStatusClaimed, items[0].Status, "item must not remain pending without a continuation")
				require.Equal(t, 1, items[0].AutomaticAttemptCount, "first claim must consume exactly one attempt")
				var epoch int64
				var cycles int
				require.NoError(t, pool.QueryRow(ctx, `SELECT feedback_bot_epoch,feedback_bot_cycles_in_epoch FROM pull_requests WHERE org_id=$1 AND id=$2`, org, pr).Scan(&epoch, &cycles), "read persisted bot budget")
				expectedEpoch, expectedCycles := int64(4), 0
				if tt.author == models.PRFeedbackAuthorTypeBot {
					expectedEpoch, expectedCycles = 3, 3
				}
				require.Equal(t, []int64{expectedEpoch, int64(expectedCycles)}, []int64{epoch, int64(cycles)}, "human intervention must reset budget and bots must consume one bounded cycle")
			}
			_, err = store.GetBatch(ctx, uuid.New(), batch.ID)
			require.Error(t, err, "foreign tenant must not read feedback snapshot")
		})
	}
}

func TestPRFeedbackContinuationAndResponsePostgres(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	tests := []struct {
		name    string
		foreign bool
	}{{"owned continuation", false}, {"foreign continuation rejected", true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			org, pr, session := seedFeedbackLifecycle(t, pool)
			store := NewPullRequestFeedbackStore(pool)
			store.SetJobStore(NewJobStore(pool))
			item := models.PullRequestFeedbackItem{PullRequestID: pr, Surface: models.PRFeedbackSurfaceIssueComment, ProviderObjectID: 17, AuthorLogin: "tester", AuthorType: models.PRFeedbackAuthorTypeUser, Body: "Fix the nil check", BodyHash: "hash-17", Intent: models.PRFeedbackIntentChangeRequest, Status: models.PRFeedbackItemStatusPending}
			require.NoError(t, store.UpsertItem(ctx, org, &item), "seed pending feedback")
			batch, err := store.ClaimBatch(ctx, org, pr, time.Now())
			require.NoError(t, err, "claim exact pending feedback atomically")
			queryOrg := org
			if tt.foreign {
				queryOrg = uuid.New()
			}
			running, err := store.QueueContinuation(ctx, queryOrg, batch.ID, "Please fix the nil check.", "Bounded structured feedback.")
			if tt.foreign {
				require.Error(t, err, "foreign tenant cannot enqueue work against owned batch")
				unchanged, err := store.GetBatch(ctx, org, batch.ID)
				require.NoError(t, err, "read protected batch")
				require.Equal(t, models.PRFeedbackBatchStatusQueued, unchanged.Status, "failed tenant fence must leave batch queued")
				return
			}
			require.NoError(t, err, "continuation must commit message, inbox, job and batch together")
			require.Equal(t, models.PRFeedbackBatchStatusRunning, running.Status, "enqueued batch must be running")
			require.NotNil(t, running.ThreadID, "continuation must bind canonical feedback thread")
			repeated, err := store.QueueContinuation(ctx, org, batch.ID, "Duplicate summary", "Duplicate prompt")
			require.NoError(t, err, "duplicate continuation should be idempotent")
			require.Equal(t, running, repeated, "duplicate must preserve all committed state")
			var messages, jobs, inbox int
			require.NoError(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM session_messages WHERE org_id=$1 AND session_id=$2 AND thread_id=$3),(SELECT count(*) FROM jobs WHERE org_id=$1 AND dedupe_key=$4),(SELECT count(*) FROM thread_inbox_entries WHERE org_id=$1 AND session_id=$2 AND thread_id=$3)`, org, session, *running.ThreadID, "continue_pr_feedback:"+batch.ID.String()).Scan(&messages, &jobs, &inbox), "inspect durable continuation components")
			require.Equal(t, []int{1, 1, 1}, []int{messages, jobs, inbox}, "repeated continuation must produce exactly one message, job and inbox entry")
			sha := "head-after"
			changed, err := store.CompleteAgent(ctx, org, batch.ID, "Fixed nil check", &sha, models.PRFeedbackBatchStatusResponding)
			require.NoError(t, err, "complete agent against running state")
			require.True(t, changed, "first agent completion must advance batch")
			changed, err = store.CompleteAgent(ctx, org, batch.ID, "Duplicate result", &sha, models.PRFeedbackBatchStatusResponding)
			require.NoError(t, err, "repeat completion must remain harmless")
			require.False(t, changed, "terminal agent completion must not overwrite first result")
			responded, err := store.RecordResponse(ctx, org, item.ID, 99, "Fixed in head-after", &sha)
			require.NoError(t, err, "record exact provider response receipt")
			require.True(t, responded, "first response receipt must be stored")
			responded, err = store.RecordResponse(ctx, org, item.ID, 100, "Duplicate reply", &sha)
			require.NoError(t, err, "duplicate provider receipt must be fenced")
			require.False(t, responded, "response must not be recorded twice")
			items, err := store.ListBatchItems(ctx, org, batch.ID)
			require.NoError(t, err, "reload responded feedback")
			require.Equal(t, models.PRFeedbackItemStatusResponded, items[0].Status, "durable item must record publication")
			require.Equal(t, int64(99), *items[0].GitHubResponseCommentID, "original exact receipt must win duplicate publication reconciliation")
			active, err := store.GetActiveBatch(ctx, org, pr)
			require.NoError(t, err, "responding remains active")
			require.Equal(t, batch.ID, active.ID, "active writer must remain exact batch")
			advanced, err := store.AdvanceBatch(ctx, org, batch.ID, models.PRFeedbackBatchStatusResponding, models.PRFeedbackBatchStatusCompleted)
			require.NoError(t, err, "complete responding batch")
			require.True(t, advanced, "only expected prior state may advance")
		})
	}
}

func TestPRFeedbackRecoveryFencesPostgres(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	tests := []struct {
		name              string
		foreign, terminal bool
	}{{"owned pending work", false, false}, {"foreign recovery rejected", true, false}, {"completed work cannot retry", false, true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			org, pr, _ := seedFeedbackLifecycle(t, pool)
			store := NewPullRequestFeedbackStore(pool)
			root := int64(90)
			item := models.PullRequestFeedbackItem{PullRequestID: pr, Surface: models.PRFeedbackSurfaceReviewComment, ProviderObjectID: 90, GitHubThreadRootCommentID: &root, AuthorLogin: "reviewer", AuthorType: models.PRFeedbackAuthorTypeBot, Body: "Check errors", BodyHash: "root-hash", Intent: models.PRFeedbackIntentChangeRequest, Status: models.PRFeedbackItemStatusPending}
			require.NoError(t, store.UpsertItem(ctx, org, &item), "seed recovery source item")
			batch, err := store.ClaimBatch(ctx, org, pr, time.Now())
			require.NoError(t, err, "claim original feedback")
			_, err = pool.Exec(ctx, `UPDATE pull_request_feedback_batches SET status=$1,error_code='provider_error',completed_at=now() WHERE org_id=$2 AND id=$3`, func() string {
				if tt.terminal {
					return "completed"
				}
				return "needs_attention"
			}(), org, batch.ID)
			require.NoError(t, err, "seed exact terminal recovery state")
			queryOrg := org
			if tt.foreign {
				queryOrg = uuid.New()
			}
			retried, err := store.RetryBatch(ctx, queryOrg, pr, batch.ID, "verified-head")
			if tt.foreign {
				require.ErrorIs(t, err, pgx.ErrNoRows, "foreign tenant cannot reset feedback epoch")
			} else {
				require.NoError(t, err, "recovery must fence non-attention work")
				require.Equal(t, !tt.terminal, retried, "only needs-attention batch may retry")
			}
			fresh, err := store.GetBatch(ctx, org, batch.ID)
			require.NoError(t, err, "reload recovered batch")
			expectedStatus := models.PRFeedbackBatchStatusNeedsAttention
			if tt.terminal {
				expectedStatus = models.PRFeedbackBatchStatusCompleted
			}
			if retried {
				expectedStatus = models.PRFeedbackBatchStatusQueued
				require.Equal(t, "verified-head", fresh.ExpectedHeadSHA, "retry must be pinned to caller-verified head")
				require.Nil(t, fresh.ErrorCode, "retry must clear old failure")
				require.Nil(t, fresh.CompletedAt, "retry must become active again")
			}
			require.Equal(t, expectedStatus, fresh.Status, "recovery must preserve protected states")
			monitoringErr := store.UpdateMonitoring(ctx, queryOrg, pr, models.PRFeedbackMonitoringDisabled)
			if tt.foreign {
				require.ErrorIs(t, monitoringErr, pgx.ErrNoRows, "foreign tenant cannot change feedback monitoring")
			} else {
				require.NoError(t, monitoringErr, "owned PR monitoring should update")
			}
			pending, attention, err := store.CountItemsByState(ctx, queryOrg, pr)
			require.NoError(t, err, "state counts must remain tenant-scoped")
			require.Equal(t, []int{0, 0}, []int{pending, attention}, "claimed source must not be counted as runnable pending work")
			decisionErr := store.SetItemDecision(ctx, queryOrg, item.ID, models.PRFeedbackIntentQuestion, models.PRFeedbackItemStatusIgnored, nil, nil, models.PRFeedbackBotEligibilitySource(""))
			if tt.foreign {
				require.ErrorIs(t, decisionErr, pgx.ErrNoRows, "foreign decision must fail tenant fence")
			} else {
				require.NoError(t, decisionErr, "owned eligibility decision should persist")
			}
			recent, err := store.ListRecentItems(ctx, queryOrg, pr, 0)
			require.NoError(t, err, "bounded history should be readable")
			if tt.foreign {
				require.Empty(t, recent, "history must omit foreign tenant item")
			} else {
				require.Equal(t, models.PRFeedbackItemStatusIgnored, recent[0].Status, "eligibility decision must be durable")
				require.Equal(t, "root-hash", *recent[0].ProcessedBodyHash, "ignored item must record exact processed body to fence redelivery")
			}
			remaining, err := store.ListPendingItems(ctx, queryOrg, pr, 101)
			require.NoError(t, err, "pending limit fallback should remain bounded")
			require.Empty(t, remaining, "claimed or ignored sources must not become pending without a body edit")
			cancelled, err := store.CancelPendingThread(ctx, queryOrg, pr, root)
			require.NoError(t, err, "resolved thread cancellation should be safe on nonpending work")
			require.Equal(t, int64(0), cancelled, "resolved thread must not cancel already ignored item")
		})
	}
}
