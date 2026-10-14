package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type reviewWorkspacePreparerFunc func(context.Context, *models.Session, string) (*agent.Sandbox, error)

func (f reviewWorkspacePreparerFunc) PrepareCodeReviewWorkspace(ctx context.Context, session *models.Session, head string) (*agent.Sandbox, error) {
	return f(ctx, session, head)
}

func seedWorkspacePreparationJob(t *testing.T, pool *pgxpool.Pool, f strandedReviewFixture, startedAt time.Time, status models.JobStatus, detail string) (uuid.UUID, uuid.UUID, string, json.RawMessage) {
	t.Helper()
	id, token := uuid.New(), uuid.New()
	node := "review-preparation-" + id.String()
	raw, err := json.Marshal(prepareCodeReviewWorkspacePayload{OrgID: f.job.OrgID, MetadataID: f.job.MetadataID, SessionID: f.job.SessionID, ExpectedGeneration: 0})
	require.NoError(t, err, "encode exact initializer provenance")
	_, err = pool.Exec(f.ctx(), `INSERT INTO nodes(id,mode,status,last_heartbeat_at) VALUES($1,'worker','active',now())`, node)
	require.NoError(t, err, "seed isolated healthy preparation owner")
	_, err = pool.Exec(f.ctx(), `UPDATE sessions SET revision_context='{"head_sha":"head","github_repo":"test/saved-review"}' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID)
	require.NoError(t, err, "pin the review's source identity before preparing")
	_, err = pool.Exec(f.ctx(), `INSERT INTO jobs(id,org_id,queue,job_type,payload,status,dedupe_key,lock_token,locked_by_node_id,lease_expires_at,created_at,last_error) VALUES($1,$2,'agent','prepare_code_review_workspace',$3,$4,$5,$6,$7,now()+interval '10 minutes',$8,$9)`, id, f.job.OrgID, raw, status, codeReviewWorkspacePreparationKey(f.job.MetadataID, f.job.SessionID, 0), token, node, startedAt, detail)
	require.NoError(t, err, "persist the original preparation checkpoint and current-generation signal")
	return id, token, node, raw
}

//nolint:paralleltest // Migrate once before parallel isolated-tenant cases to bound PostgreSQL DDL locks.
func TestCodeReviewWorkspaceInitializerRetryPostgres(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for initializer retry proof")
	}
	pool := fullRecoveryPostgresPool(t, context.Background())
	tests := []struct {
		name, stage string
		cause       error
		retry       bool
		expire      bool
		late        bool
	}{
		{name: "transient clone recovers once", stage: agent.CodeReviewPreparationStageClone, cause: errors.New("fatal: Could not resolve host: github.com"), retry: true},
		{name: "transient hydration exhausts original budget", stage: agent.CodeReviewPreparationStageHydration, cause: errors.New("the requested URL returned error: 500"), retry: true, expire: true},
		{name: "generic clone error returned after deadline stays terminal", stage: agent.CodeReviewPreparationStageClone, cause: errors.New("connection reset by peer"), late: true},
		{name: "deterministic bootstrap stops once", stage: agent.CodeReviewPreparationStageDependencySetup, cause: errors.New("curl 28 Operation timed out after 60000 milliseconds")},
		{name: "malformed hydration revision stops once", stage: agent.CodeReviewPreparationStageHydration, cause: errors.New("invalid pinned head SHA")},
		{name: "moved head stops initializer once", stage: agent.CodeReviewPreparationStageCheckout, cause: fmt.Errorf("%w: private diagnostic", agent.ErrStalePullRequestHead)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedStrandedReview(t, pool, "running")
			started := time.Now().UTC().Add(-time.Minute)
			if tt.late {
				started = time.Now().UTC().Add(-codeReviewWorkspaceWaitLimit + time.Second)
			}
			id, token, node, raw := seedWorkspacePreparationJob(t, pool, f, started, models.JobStatusRunning, "")
			// PostgreSQL stores microsecond precision; retry authority is exactly
			// the timestamp read back from the original durable checkpoint.
			require.NoError(t, pool.QueryRow(f.ctx(), `SELECT created_at FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, id).Scan(&started), "load persisted preparation timestamp precision")
			calls := 0
			preparer := reviewWorkspacePreparerFunc(func(_ context.Context, _ *models.Session, head string) (*agent.Sandbox, error) {
				calls++
				require.Equal(t, "head", head, "retries must preserve the pinned review revision")
				if calls == 1 {
					if tt.late {
						time.Sleep(1100 * time.Millisecond)
					}
					return nil, agent.NewCodeReviewWorkspacePreparationError(tt.stage, tt.cause)
				}
				return &agent.Sandbox{ID: "prepared-" + id.String()}, nil
			})
			services := &Services{ReviewPreparationPolicy: workspacePreparationTestPolicy(true), CodeReviewWorkspacePreparer: preparer, SandboxProvider: &stubSandboxProvider{}}
			handler := newPrepareCodeReviewWorkspaceHandler(f.stores, services, zerolog.Nop())
			ctx := jobctx.WithWorkerNodeID(jobctx.WithLockToken(jobctx.WithJobID(context.Background(), id), token), node)
			actual := handler(ctx, models.JobTypePrepareCodeReviewWorkspace, raw)
			if tt.retry {
				var retry *RetryableError
				require.ErrorAs(t, actual, &retry, "transient source failure should keep the same initializer retryable")
				require.Equal(t, &started, retry.RetryWindowStartedAt, "initializer retry must inherit the earliest durable checkpoint")
				if tt.expire {
					_, err := pool.Exec(f.ctx(), `UPDATE jobs SET created_at=now()-interval '9 minutes' WHERE org_id=$1 AND id=$2`, f.job.OrgID, id)
					require.NoError(t, err, "advance only the isolated preparation fixture beyond its deadline")
					err = handler(ctx, models.JobTypePrepareCodeReviewWorkspace, raw)
					var fatal *FatalError
					require.ErrorAs(t, err, &fatal, "expired retry must terminalize before another clone or bootstrap")
					require.ErrorIs(t, err, context.DeadlineExceeded, "the original budget must own the terminal outcome")
					require.Equal(t, 1, calls, "deadline exhaustion must not start another initializer")
				} else {
					require.NoError(t, handler(ctx, models.JobTypePrepareCodeReviewWorkspace, raw), "transient source retry should publish a prepared workspace through the existing lease fence")
					require.Equal(t, 2, calls, "successful retry should perform exactly one additional preparation")
					ready, err := f.stores.CodeReviewWorkspaces.Readiness(f.ctx(), f.job.OrgID, f.job.MetadataID, f.job.SessionID, f.job.HeadSHA)
					require.NoError(t, err, "read published workspace readiness")
					require.True(t, ready.Ready, "retry success must have a real fenced holder before fan-out")
				}
			} else {
				var fatal *FatalError
				require.ErrorAs(t, actual, &fatal, "failed bootstrap and deterministic input may not repeat")
				if tt.late {
					require.ErrorIs(t, actual, context.DeadlineExceeded, "a non-context error returned after the original deadline must terminalize as budget exhaustion")
					require.Equal(t, "Review workspace preparation exceeded its total eight-minute budget during repository clone. Review did not run.", fatal.Error(), "late clone failures should retain exact safe stage and budget guidance")
				}
				_, err := pool.Exec(f.ctx(), `UPDATE jobs SET status='dead_letter',last_error=$3 WHERE org_id=$1 AND id=$2`, f.job.OrgID, id, fatal.Error())
				require.NoError(t, err, "persist finite terminal initializer diagnostics as the worker does")
				gateErr := ensureCodeReviewWorkspaceReady(f.ctx(), f.stores, services, zerolog.Nop(), f.job)
				var failure *codeReviewWorkspaceGateFailure
				require.ErrorAs(t, gateErr, &failure, "terminal initializer must stop the controller before fan-out")
				require.Equal(t, errors.Is(tt.cause, agent.ErrStalePullRequestHead), errors.Is(gateErr, agent.ErrStalePullRequestHead), "moved-head signal must survive persistence as supersession")
				require.Equal(t, 1, calls, "the gate must never invoke preparation after deterministic or moved-head failure")
			}
		})
	}
}

//nolint:paralleltest // Migrate once before parallel isolated-tenant controller cases.
func TestCodeReviewMovedWorkspaceHeadControllerPostgres(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for moved-head controller proof")
	}
	pool := fullRecoveryPostgresPool(t, context.Background())
	tests := []struct {
		name           string
		changed        bool
		syncError      bool
		expired        bool
		webhookStale   bool
		generationRace bool
	}{
		{name: "live head change queues confirmed replacement", changed: true},
		{name: "API lag waits then settles stale"},
		{name: "sync failure waits then settles stale", syncError: true},
		{name: "expired signal settles without live call", expired: true},
		{name: "webhook already superseded stops without sync", webhookStale: true},
		{name: "generation changes during live sync stays fenced", changed: true, generationRace: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedStrandedReview(t, pool, "running")
			started := time.Now().UTC().Add(-time.Minute)
			if tt.expired {
				started = started.Add(-codeReviewWorkspaceWaitLimit)
			}
			id, _, _, _ := seedWorkspacePreparationJob(t, pool, f, started, models.JobStatusDeadLetter, agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageCheckout, agent.ErrStalePullRequestHead).Error())
			if tt.webhookStale {
				_, err := f.stores.CodeReviews.MarkStale(f.ctx(), f.job.OrgID, f.job.SessionID, "already superseded by webhook")
				require.NoError(t, err, "seed a webhook that already replaced the old review")
			}
			syncCalls := 0
			lifecycle := &codeReviewLifecycleStub{result: codereviewsvc.ReviewRequestedResult{Processed: true}}
			services := &Services{ReviewPreparationPolicy: workspacePreparationTestPolicy(true), CodeReviewLifecycle: lifecycle, PR: &stubPRService{syncPullRequestStateFn: func(ctx context.Context, org, pr uuid.UUID) error {
				syncCalls++
				deadline, hasDeadline := ctx.Deadline()
				require.True(t, hasDeadline, "live synchronization must have the original preparation deadline")
				require.WithinDuration(t, started.Add(codeReviewWorkspaceWaitLimit), deadline, time.Microsecond, "live-sync context must spend only the remaining original budget")
				if tt.syncError {
					return errors.New("raw token=synthetic-secret")
				}
				if tt.changed {
					_, err := pool.Exec(ctx, `UPDATE pull_requests SET head_sha='confirmed-new-head',base_sha='confirmed-base' WHERE org_id=$1 AND id=$2`, org, pr)
					require.NoError(t, err, "live sync should capture the confirmed replacement revision")
				}
				if tt.generationRace {
					_, err := pool.Exec(ctx, `UPDATE sessions SET workspace_generation=workspace_generation+1 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID)
					require.NoError(t, err, "simulate a concurrent workspace generation advance")
				}
				return nil
			}}}
			raw, err := json.Marshal(f.job)
			require.NoError(t, err, "encode the original full-review controller")
			handler := newRunCodeReviewHandler(f.stores, services, zerolog.Nop())
			actual := handler(f.ctx(), models.JobTypeRunCodeReview, raw)
			if (!tt.changed && !tt.expired && !tt.webhookStale) || tt.generationRace {
				var retry *RetryableError
				require.ErrorAs(t, actual, &retry, "unresolved or raced moved-head state must not approve, fan out, or become worker_failed")
				if !tt.generationRace {
					require.Equal(t, durationPtr(codeReviewWorkspaceWaitLimit), retry.MaxRetryDuration, "moved-head wait must share the initial preparation limit")
					_, err := pool.Exec(f.ctx(), `UPDATE jobs SET created_at=now()-interval '9 minutes' WHERE org_id=$1 AND id=$2`, f.job.OrgID, id)
					require.NoError(t, err, "exhaust the same durable checkpoint without enqueuing another initializer")
					require.NoError(t, handler(f.ctx(), models.JobTypeRunCodeReview, raw), "expired moved-head wait should settle stale even when live sync cannot recover")
				}
			} else {
				require.NoError(t, actual, "confirmed head changes and expired/superseded work should settle normally")
			}
			metadata, err := f.stores.CodeReviews.GetBySessionID(f.ctx(), f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read controller outcome after moved-head reconciliation")
			wantStatus := models.CodeReviewSessionStatusStale
			if tt.generationRace {
				wantStatus = models.CodeReviewSessionStatusRunning
			}
			require.Equal(t, wantStatus, metadata.Status, "moved revision must remain stale or fenced rather than operationally failed")
			require.False(t, metadata.StatusCode != nil && *metadata.StatusCode == models.CodeReviewStatusCodeWorkerFailed, "head movement cannot be misreported as worker failure")
			require.NotContains(t, stringPtrValue(metadata.FailureReason), "synthetic-secret", "raw sync causes may never appear in author-facing failure text")
			var preparations, threads int
			require.NoError(t, pool.QueryRow(f.ctx(), `SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type='prepare_code_review_workspace' AND payload->>'session_id'=$2`, f.job.OrgID, f.job.SessionID.String()).Scan(&preparations), "count exact-session initializer jobs")
			require.Equal(t, 1, preparations, "moved-head retries must never reenqueue initialization")
			require.NoError(t, pool.QueryRow(f.ctx(), `SELECT count(*) FROM session_threads WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID).Scan(&threads), "count reviewer dispatches")
			require.Equal(t, 0, threads, "a moved-head signal may never bypass actual workspace readiness")
			wantSync := 1
			if tt.expired || tt.webhookStale {
				wantSync = 0
			}
			require.Equal(t, wantSync, syncCalls, "expiry and existing webhook supersession must stop additional live calls")
			wantQueues := 0
			if tt.changed && !tt.generationRace {
				wantQueues = 1
				require.Equal(t, "confirmed-new-head", lifecycle.queuedInput.HeadSHA, "replacement must use only the live confirmed head")
				require.False(t, lifecycle.queuedInput.ExplicitRequest, "automatic supersession must retain its existing ensure-current semantics")
			}
			require.Equal(t, wantQueues, lifecycle.queueCalls, "API lag or sync failure must not invent a new review or reset its budget")
		})
	}
}

//nolint:paralleltest // Migrate once before isolated-tenant hook fence cases.
func TestCodeReviewMovedHeadDeadLetterFencePostgres(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for moved-head dead-letter fence proof")
	}
	pool := fullRecoveryPostgresPool(t, context.Background())
	tests := []struct {
		name, setup string
		unscoped    bool
		stale       bool
	}{
		{name: "original moved generation settles stale", stale: true},
		{name: "older signal cannot adopt newer generation", setup: "generation"},
		{name: "publication receipt stays protected", setup: "receipt"},
		{name: "unscoped stale error cannot settle current review", unscoped: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedStrandedReview(t, pool, "running")
			started := time.Now().Add(-9 * time.Minute)
			seedWorkspacePreparationJob(t, pool, f, started, models.JobStatusDeadLetter, agent.NewCodeReviewWorkspacePreparationError(agent.CodeReviewPreparationStageCheckout, agent.ErrStalePullRequestHead).Error())
			if tt.setup == "generation" {
				_, err := pool.Exec(f.ctx(), `UPDATE sessions SET workspace_generation=1 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID)
				require.NoError(t, err, "advance generation after the original runtime observed its signal")
			} else if tt.setup == "receipt" {
				_, err := pool.Exec(f.ctx(), `UPDATE code_review_revision_assessments SET publication_receipt='{}' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
				require.NoError(t, err, "protect the assessment before stale runtime reconciliation")
			}
			ctx := jobctx.WithDeadLetterHooks(f.ctx())
			registerCodeReviewDeadLetterReconciliation(ctx, f.stores, &Services{}, zerolog.Nop(), f.job)
			var failure = codeReviewMovedHeadRetry(errors.New("raw token=synthetic-secret"), started, 0)
			if tt.unscoped {
				failure = agent.ErrStalePullRequestHead
			}
			jobctx.RunDeadLetterHooks(ctx, failure)
			metadata, err := f.stores.CodeReviews.GetBySessionID(f.ctx(), f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read durable metadata after runtime exhaustion")
			assessment, err := f.stores.CodeReviewAssessments.GetBySessionID(f.ctx(), f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read assessment after the atomic metadata settlement fence")
			wantMetadata, wantAssessment := models.CodeReviewSessionStatusRunning, models.CodeReviewAssessmentRunning
			if tt.stale {
				wantMetadata, wantAssessment = models.CodeReviewSessionStatusStale, models.CodeReviewAssessmentSuperseded
			}
			require.Equal(t, wantMetadata, metadata.Status, "an old or unscoped error may never infer newer ownership or replace publication")
			require.Equal(t, wantAssessment, assessment.Status, "assessment supersession must follow rather than precede the atomic generation/publication guard")
			require.NotContains(t, stringPtrValue(metadata.FailureReason), "synthetic-secret", "raw runtime error text must not enter author-facing revision status")
		})
	}
}
