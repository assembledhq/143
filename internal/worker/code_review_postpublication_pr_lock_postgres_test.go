package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// This provider is a synchronized logical boundary, not network-delivery proof.
// The existing fake owns request/result construction; all SQL and settlement
// below are the real full-handler, worker and database implementations.
type postpublicationPRLockPublisher struct {
	mu sync.Mutex
	inlineRecoveryPublisher
}

func (p *postpublicationPRLockPublisher) ListPullRequestFiles(ctx context.Context, request codereviewsvc.PullRequestFilesRequest) ([]codereviewsvc.PullRequestFile, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inlineRecoveryPublisher.ListPullRequestFiles(ctx, request)
}

func (p *postpublicationPRLockPublisher) SubmitReview(ctx context.Context, request codereviewsvc.SubmitReviewRequest) (codereviewsvc.SubmitReviewResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inlineRecoveryPublisher.SubmitReview(ctx, request)
}

func (p *postpublicationPRLockPublisher) submitted() ([]codereviewsvc.SubmitReviewRequest, []codereviewsvc.PullRequestFilesRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]codereviewsvc.SubmitReviewRequest(nil), p.requests...), append([]codereviewsvc.PullRequestFilesRequest(nil), p.fileRequests...)
}

type postpublicationPRLockAttempt struct {
	pid       uint32
	policy    [4]string
	deadline  time.Time
	remaining time.Duration
	err       error
}

type postpublicationPRLockRunResult struct {
	err      error
	attempts []postpublicationPRLockAttempt
}

// Gate the completion phase only after entry reconciliation has finished and
// the original assessment has a real confirmed receipt. Earlier units retain
// the supplied runner unchanged, including on already-confirmed repaired polls.
type postpublicationPRLockRun struct {
	base       db.CodeReviewMetadataTransactions
	mu         sync.Mutex
	ready      func(context.Context) (bool, error)
	gated      bool
	calls      int // Counts only the selected completion gate.
	entered    chan struct{}
	start      chan struct{}
	returnGate chan struct{}
	attempt    chan postpublicationPRLockAttempt
	returned   chan postpublicationPRLockRunResult
}

// Observe the exact real GetBySessionID read used by recoverStagedFullAssessment.
// Future selector drift fails closed: no matching successful read means no gate.
const postpublicationAssessmentEntryQuery = "SELECT * FROM code_review_revision_assessments WHERE org_id=$1 AND session_id=$2 AND review_scope='full' ORDER BY created_at DESC,id DESC LIMIT 1"

type postpublicationAssessmentEntryDB struct {
	db.DBTX
	orgID, sessionID uuid.UUID
	ready            *atomic.Bool
}

func (d postpublicationAssessmentEntryDB) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	rows, err := d.DBTX.Query(ctx, query, args...)
	if err != nil || query != postpublicationAssessmentEntryQuery || len(args) != 2 {
		return rows, err
	}
	orgID, orgOK := args[0].(uuid.UUID)
	sessionID, sessionOK := args[1].(uuid.UUID)
	if !orgOK || !sessionOK || orgID != d.orgID || sessionID != d.sessionID {
		return rows, nil
	}
	return &postpublicationAssessmentEntryRows{Rows: rows, ready: d.ready}, nil
}

type postpublicationAssessmentEntryRows struct {
	pgx.Rows
	ready               *atomic.Bool
	scanned, scanFailed bool
}

func (rows *postpublicationAssessmentEntryRows) Scan(dest ...any) error {
	err := rows.Rows.Scan(dest...)
	if err != nil {
		rows.scanFailed = true
	} else {
		rows.scanned = true
	}
	return err
}

func (rows *postpublicationAssessmentEntryRows) Close() {
	rows.Rows.Close()
	if rows.scanned && !rows.scanFailed && rows.Rows.Err() == nil {
		rows.ready.Store(true)
	}
}

func newPostpublicationPRLockRun(base db.CodeReviewMetadataTransactions, pool *pgxpool.Pool, f strandedReviewFixture) *postpublicationPRLockRun {
	stageReady := new(atomic.Bool)
	f.stores.CodeReviewAssessments = db.NewCodeReviewAssessmentStore(postpublicationAssessmentEntryDB{
		DBTX: pool, orgID: f.job.OrgID, sessionID: f.job.SessionID, ready: stageReady,
	})
	return &postpublicationPRLockRun{base: base, ready: func(ctx context.Context) (bool, error) {
		if !stageReady.Load() {
			return false, nil
		}
		var confirmed bool
		err := pool.QueryRow(ctx, `SELECT publication_state='confirmed' AND publication_receipt IS NOT NULL
 FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment).Scan(&confirmed)
		return confirmed, err
	}, entered: make(chan struct{}, 1), start: make(chan struct{}), returnGate: make(chan struct{}),
		attempt: make(chan postpublicationPRLockAttempt, 3), returned: make(chan postpublicationPRLockRunResult, 1)}
}

func (r *postpublicationPRLockRun) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	r.mu.Lock()
	if r.gated {
		r.mu.Unlock()
		return r.base.Run(ctx, work)
	}
	ready, err := r.ready(ctx)
	if err != nil {
		r.mu.Unlock()
		return err
	}
	if !ready {
		r.mu.Unlock()
		return r.base.Run(ctx, work)
	}
	r.gated = true
	r.calls++
	r.mu.Unlock()
	select {
	case r.entered <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-r.start:
	case <-ctx.Done():
		return ctx.Err()
	}
	var attempts []postpublicationPRLockAttempt
	err = r.base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		observation := postpublicationPRLockAttempt{pid: tx.Conn().PgConn().PID()}
		observation.deadline, _ = unitCtx.Deadline()
		observation.remaining = time.Until(observation.deadline)
		if err := tx.QueryRow(unitCtx, `SELECT current_setting('transaction_isolation'),
			current_setting('lifecycle143.writer_version',true),current_setting('lock_timeout'),
			current_setting('statement_timeout')`).Scan(&observation.policy[0], &observation.policy[1], &observation.policy[2], &observation.policy[3]); err != nil {
			return err
		}
		select {
		case r.attempt <- observation:
		case <-unitCtx.Done():
			return unitCtx.Err()
		}
		observation.err = work(unitCtx, tx)
		attempts = append(attempts, observation)
		return observation.err
	})
	// Run has already settled and released its checkout before this signal.
	r.returned <- postpublicationPRLockRunResult{err: err, attempts: attempts}
	select {
	case <-r.returnGate:
	case <-ctx.Done():
	}
	return err
}

type postpublicationPRLockGraph struct {
	fullAssessmentDispatchGraph
	requests, guards json.RawMessage
}

func postpublicationPRLockSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f strandedReviewFixture) postpublicationPRLockGraph {
	t.Helper()
	g := postpublicationPRLockGraph{fullAssessmentDispatchGraph: fullAssessmentDispatchSnapshot(t, ctx, pool, f)}
	require.NoError(t, pool.QueryRow(ctx, `SELECT
		COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM code_review_requests r WHERE r.org_id=$1),'[]'::jsonb),
		jsonb_build_object(
		 'runtimes',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM thread_runtimes r WHERE r.org_id=$1 AND r.session_id=$2),'[]'::jsonb),
		 'executors',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM session_executors r WHERE r.org_id=$1 AND r.session_id=$2),'[]'::jsonb),
		 'holders',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM session_sandbox_holders r WHERE r.org_id=$1 AND r.session_id=$2),'[]'::jsonb),
		 'previews',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM preview_instances r WHERE r.org_id=$1 AND r.session_id=$2),'[]'::jsonb),
		 'dispatches',COALESCE((SELECT jsonb_agg(to_jsonb(r) ORDER BY r.assessment_id) FROM code_review_recheck_dispatches r WHERE r.org_id=$1 AND r.session_id=$2),'[]'::jsonb))`, f.job.OrgID, f.job.SessionID).Scan(&g.requests, &g.guards),
		"retain all request rows and execution guards alongside the complete original owner/evidence/job graph")
	return g
}

func postpublicationPRLockJob(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org, id uuid.UUID) map[string]json.RawMessage {
	t.Helper()
	var raw json.RawMessage
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_jsonb(j) FROM jobs j WHERE j.org_id=$1 AND j.id=$2`, org, id).Scan(&raw), "read every field of the exact original or repaired controller")
	return fullAssessmentDispatchObject(t, raw)
}

// Compare every column, including future added columns. Callers name only the
// known transition fields and separately assert their concrete new values.
func postpublicationPRLockRow(t *testing.T, before, after map[string]json.RawMessage, changes map[string]json.RawMessage) {
	t.Helper()
	want := make(map[string]json.RawMessage, len(before))
	for key, value := range before {
		want[key] = value
	}
	for key, value := range changes {
		_, present := before[key]
		require.True(t, present, "expected transition column %s must exist in the complete baseline row", key)
		want[key] = value
	}
	require.Equal(t, want, after, "only the explicit, positively asserted transition fields may change in the complete row")
}

type postpublicationPRLockClaim struct{ jobID, token uuid.UUID }

type postpublicationPRLockHook struct {
	claim      postpublicationPRLockClaim
	committed  bool
	pending    bool
	scope      db.CodeReviewTerminalScope
	jobStatus  models.JobStatus
	metadata   models.CodeReviewSessionMetadata
	assessment models.CodeReviewAssessment
	schedule   models.CodeReviewPRState
	err        error
}

//nolint:paralleltest // Each leaf owns a fully migrated database and an external PostgreSQL lock schedule; serialize migration/repair sweeps and bounded holder cleanup.
func TestPostpublicationFullHandlerPRLockRecoveryPostgres(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for real postpublication completion contention and recovery")
	}
	tests := []struct {
		name        string
		exhausted   bool
		maxAttempts int
	}{
		{name: "ordinary original-controller retry preserves the confirmed publication", maxAttempts: 3},
		{name: "atomic retirement and existing repair drain the original confirmed publication", exhausted: true, maxAttempts: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// No t.Parallel: this test intentionally serializes its isolated real
			// PostgreSQL migration, schema-wide repair and holder/contender schedule.
			pool := fullRecoveryPostgresPoolWithMaxConns(t, context.Background(), 3)
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			require.Equal(t, int32(3), pool.Config().MaxConns, "the contended leaf has only holder, real contender and warmed observer capacity")
			f := seedStrandedReview(t, pool, "idle")
			seedSavedReviewResults(t, pool, f)
			findingID, requestID := uuid.New(), uuid.New()
			finding := models.CodeReviewFinding{Path: stringPtr("file.go"), StartLine: intPtr(347), EndLine: intPtr(354), Severity: models.CodeReviewFindingSeverityHigh, Summary: "Retained synthetic blocker", Body: "Fix this retained defect", DedupeKey: "postpublication-stable-key"}
			body := models.BuildCodeReviewFinalReviewBody(models.CodeReviewFinalReviewInput{Decision: models.CodeReviewDecisionNeedsHumanReview, Findings: []models.CodeReviewFinding{finding}})
			for _, seed := range []struct {
				query string
				args  []any
			}{
				{`UPDATE code_review_revision_assessments SET result_origin='executed',coverage_complete=true,decision='needs_human_review',acceptable=false,risk_reason_details='[]',structured_outcome='{"coverage_complete":true,"description_assessments":[]}',rendered_body=$3 WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.assessment, body}},
				{`UPDATE code_review_pr_state SET generation=1,active_assessment_id=$3 WHERE org_id=$1 AND pull_request_id=$2`, []any{f.job.OrgID, f.job.PullRequestID, f.assessment}},
				{`INSERT INTO code_review_findings(id,org_id,session_id,dedupe_key,severity,confidence,path,start_line,end_line,summary,body,selected_for_inline) VALUES($1,$2,$3,'postpublication-stable-key','high','high','file.go',347,354,'Retained synthetic blocker','Fix this retained defect',true)`, []any{findingID, f.job.OrgID, f.job.SessionID}},
				{`INSERT INTO code_review_prompt_records(org_id,session_id,record_key,role,content) VALUES($1,$2,'postpublication-original-prompt','reviewer','Synthetic retained original prompt')`, []any{f.job.OrgID, f.job.SessionID}},
				{`INSERT INTO code_review_requests(id,org_id,repository_id,pull_request_id,source_kind,source_identity,mode,input_hash,target_generation,session_id,assessment_id,status) VALUES($1,$2,$3,$4,'api','postpublication-original-request','review_now','postpublication-input',1,$5,$6,'joined')`, []any{requestID, f.job.OrgID, f.job.RepositoryID, f.job.PullRequestID, f.job.SessionID, f.assessment}},
			} {
				_, err := pool.Exec(ctx, seed.query, seed.args...)
				require.NoError(t, err, "stage usable full output, original evidence, request and exact active reservation without a confirmed receipt")
			}
			staged, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read the full staged outcome before invoking the real handler")
			require.Equal(t, models.CodeReviewAssessmentRunning, staged.Status, "fixture begins before publication reservation")
			require.Equal(t, models.CodeReviewPublicationNotStarted, staged.PublicationState, "fixture must perform its own real publication writes")
			require.Nil(t, staged.PublicationReceipt, "fixture must not preseed the positive publication receipt")
			require.Equal(t, &body, staged.RenderedBody, "fixture explicitly chooses usable staged output")
			run := newPostpublicationPRLockRun(f.stores.ThreadSendDependencies.Transactions, pool, f)
			f.stores.ThreadSendDependencies.Transactions = run
			f.stores.CodeReviews, err = db.NewCodeReviewStoreWithTerminalTransactions(pool, run)
			require.NoError(t, err, "wire aware metadata to the actual supplied bounded runner")
			terminalJobs, err := db.NewJobStoreWithCodeReviewTerminalTransactions(pool, run)
			require.NoError(t, err, "wire aware retirement to the same actual supplied bounded runner")
			f.stores.Jobs = terminalJobs
			files := []codereviewsvc.PullRequestFile{{Filename: "file.go", Patch: "@@ -351 +351 @@\n-old\n+new", Additions: 1, Deletions: 1}}
			publisher := &postpublicationPRLockPublisher{inlineRecoveryPublisher: inlineRecoveryPublisher{files: files}}
			capture := &fixedRecheckCapture{result: codereviewsvc.AssessmentInputCaptureResult{Manifest: f.manifest, Policy: models.CodeReviewPolicyRecord{ID: f.job.PolicyID}, Files: files}}
			services := &Services{CodeReviews: publisher, CodeReviewInputCapture: capture}
			node := "postpublication-pr-lock-" + uuid.NewString()
			_, err = pool.Exec(ctx, `INSERT INTO nodes(id,mode) VALUES($1,'worker')`, node)
			require.NoError(t, err, "register the isolated consumer of the original controller")
			_, err = pool.Exec(ctx, `UPDATE jobs SET status='pending',lock_token=NULL,attempts=0,max_attempts=$3,priority=100000,target_node_id=$4,run_at=now() WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID, tt.maxAttempts, node)
			require.NoError(t, err, "make exactly the original controller claimable without replacing its payload or dedupe")
			originalJob := postpublicationPRLockJob(t, ctx, pool, f.job.OrgID, f.jobID)
			claims := make(chan postpublicationPRLockClaim, 2)
			hooks := make(chan postpublicationPRLockHook, 2)
			w := New(pool, zerolog.Nop(), node, WithCodeReviewTerminalStore(terminalJobs))
			handler := newRunCodeReviewHandler(f.stores, services, zerolog.Nop())
			w.Register(models.JobTypeRunCodeReview, func(handlerCtx context.Context, kind string, payload json.RawMessage) error {
				jobID, _ := jobctx.JobIDFromContext(handlerCtx)
				token, _ := jobctx.LockTokenFromContext(handlerCtx)
				claim := postpublicationPRLockClaim{jobID: jobID, token: token}
				select {
				case claims <- claim:
				case <-handlerCtx.Done():
					return handlerCtx.Err()
				}
				// This probe runs before the real handler's registered reconciliation
				// hook. It observes the committed outcome; it never invokes that hook.
				jobctx.RegisterDeadLetterHook(handlerCtx, func(hookCtx context.Context, _ error) {
					probe := postpublicationPRLockHook{claim: claim}
					outcome, scope, committed := committedCodeReviewTerminalOutcome(hookCtx, f.job.OrgID, models.JobTypeRunCodeReview)
					probe.scope, probe.committed, probe.pending = scope, committed, outcome.CompletionRecoveryPending()
					probe.err = pool.QueryRow(hookCtx, `SELECT status FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, jobID).Scan(&probe.jobStatus)
					if probe.err == nil {
						probe.metadata, probe.err = f.stores.CodeReviews.GetBySessionID(hookCtx, f.job.OrgID, f.job.SessionID)
					}
					if probe.err == nil {
						probe.assessment, probe.err = f.stores.CodeReviewAssessments.GetByID(hookCtx, f.job.OrgID, f.assessment)
					}
					if probe.err == nil {
						probe.schedule, probe.err = db.NewCodeReviewScheduleStore(pool).Get(hookCtx, f.job.OrgID, f.job.PullRequestID)
					}
					hooks <- probe
				})
				return handler(handlerCtx, kind, payload)
			})
			done := make(chan struct{}, 1)
			var holder pgx.Tx
			var observer *pgxpool.Conn
			var startOnce, returnOnce sync.Once
			joined, released := false, false
			defer func() {
				startOnce.Do(func() { close(run.start) })
				returnOnce.Do(func() { close(run.returnGate) })
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cleanupCancel()
				if holder != nil && !released {
					if rollbackErr := holder.Rollback(cleanupCtx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
						t.Errorf("release the test-owned PR holder during failed-assertion cleanup: %v", rollbackErr)
					}
				}
				cancel()
				if !joined {
					select {
					case <-done:
					case <-cleanupCtx.Done():
						t.Errorf("join actual Worker.poll before releasing its observer and database: %v", cleanupCtx.Err())
					}
				}
				if observer != nil {
					observer.Release()
				}
			}()
			go func() { w.poll(ctx); done <- struct{}{} }()
			select {
			case <-run.entered:
			case <-done:
				joined = true
				require.FailNow(t, "the full handler must reach completion only after publishing usable output")
			case <-ctx.Done():
				require.FailNow(t, "the full handler must reach the positive receipt gate within the case guard", "%v", ctx.Err())
			}
			var claim postpublicationPRLockClaim
			select {
			case claim = <-claims:
			case <-ctx.Done():
				require.FailNow(t, "the real claimed handler must expose its current lease", "%v", ctx.Err())
			}
			require.Equal(t, f.jobID, claim.jobID, "positive publication must belong to the exact original queue job")
			require.NotEqual(t, uuid.Nil, claim.token, "the real claim must supply a nonzero lease")
			require.NotEqual(t, f.token, claim.token, "claiming must replace the fixture's discarded preseeded token")
			published, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "independently read the durable confirmed receipt before acquiring any holder or observer slot")
			metadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "independently read metadata's exact submitted review before completion starts")
			wantReceipt, err := fullAssessmentPublicationReceipt(f.assessment, f.manifest.InputDigest, f.job.HeadSHA, new(int64(77)), new("https://example.invalid/review/77"), nil, nil)
			require.NoError(t, err, "construct the complete expected receipt for the original synthetic logical provider result")
			expectedPublished := staged
			expectedPublished.Status, expectedPublished.PublicationState = models.CodeReviewAssessmentPublishing, models.CodeReviewPublicationConfirmed
			expectedPublished.PublicationReceipt, expectedPublished.SubmittedCommitSHA = published.PublicationReceipt, &f.job.HeadSHA
			expectedPublished.GitHubReviewID, expectedPublished.GitHubReviewURL = new(int64(77)), new("https://example.invalid/review/77")
			require.JSONEq(t, string(wantReceipt), string(published.PublicationReceipt), "publication must commit the exact assessment/digest/head/review receipt before the contended unit")
			require.Equal(t, expectedPublished, published, "publication may change only its exact status, submitted head and confirmed receipt fields")
			require.True(t, confirmedFullAssessmentReceiptMatches(published, metadata), "positive receipt gate must satisfy the real exact confirmed-drain identity predicate")
			require.Equal(t, models.CodeReviewSessionStatusRunning, metadata.Status, "metadata must still be active before failed completion")
			require.Equal(t, new(int64(77)), metadata.GitHubReviewID, "metadata must already contain the actual logical submission receipt")
			require.Equal(t, &body, metadata.FinalReviewBody, "metadata must retain the original rendered body before completion")
			wantRequests := []codereviewsvc.SubmitReviewRequest{{InstallationID: 1, Repository: "test/saved-review", PullNumber: 9, HeadSHA: "head", OutputKey: f.job.OutputKey,
				Decision: codereviewsvc.SubmitReviewDecisionNeedsHumanReview, Body: body, RequirePublicationReceipt: true,
				Comments: []codereviewsvc.SubmitReviewComment{{Path: "file.go", Line: 351, Body: "[P1] Fix this retained defect", DedupeKey: "postpublication-stable-key"}}}}
			requests, fileRequests := publisher.submitted()
			require.Equal(t, wantRequests, requests, "the first logical submission must carry the entire original output identity, body, comments and receipt requirement")
			require.Equal(t, []codereviewsvc.PullRequestFilesRequest{{InstallationID: 1, Repository: "test/saved-review", PullNumber: 9}}, fileRequests, "the original synthetic diff must be restored exactly once")
			baseline := postpublicationPRLockSnapshot(t, ctx, pool, f)
			require.JSONEq(t, `{"runtimes":[],"executors":[],"holders":[],"previews":[],"dispatches":[]}`, string(baseline.guards), "positive fixture drain proof must have no runtime, executor, holder, preview or pending recheck")
			baselineParent := fullAssessmentDispatchObject(t, baseline.session)
			require.JSONEq(t, `false`, string(baselineParent["turn_holding_container"]), "the positive parent fixture must not hold an active execution turn")
			require.JSONEq(t, `null`, string(baselineParent["container_id"]), "the positive parent fixture must not retain an active workspace")
			claimedJob := postpublicationPRLockJob(t, ctx, pool, f.job.OrgID, f.jobID)
			require.JSONEq(t, `"running"`, string(claimedJob["status"]), "baseline job must be running under the real claim")
			require.JSONEq(t, `1`, string(claimedJob["attempts"]), "the real first claim must consume exactly one attempt")
			require.JSONEq(t, fmt.Sprintf("%q", claim.token.String()), string(claimedJob["lock_token"]), "baseline receipt must correlate with the actual current lease")
			require.Equal(t, originalJob["payload"], claimedJob["payload"], "the positive provider call must originate from the unchanged durable payload")
			// Only after the positive receipt gate: reserve the holder and warm a
			// different observer. During Run there is no fourth connection/pool.
			holder, err = pool.Begin(ctx)
			require.NoError(t, err, "begin the genuine long-lived PR-domain holder after publication")
			holderPID := holder.Conn().PgConn().PID()
			_, err = holder.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "code_review_pr:"+f.job.OrgID.String()+":"+f.job.PullRequestID.String())
			require.NoError(t, err, "hold the exact production PR-domain advisory key across completion Run")
			observer, err = pool.Acquire(ctx)
			require.NoError(t, err, "warm the sole independent observer after receipt durability is established")
			var observerPID uint32
			require.NoError(t, observer.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&observerPID), "warm and identify the independent PostgreSQL observer")
			require.NotEqual(t, holderPID, observerPID, "holder and independent observer must use different real PostgreSQL backends")
			startOnce.Do(func() { close(run.start) })
			var observed []postpublicationPRLockAttempt
			for attempt := 0; attempt < 3; attempt++ {
				var current postpublicationPRLockAttempt
				select {
				case current = <-run.attempt:
				case <-ctx.Done():
					require.FailNow(t, "real completion must enter each original eligible callback", "%v", ctx.Err())
				}
				require.NotEqual(t, holderPID, current.pid, "completion must use its own actual contender backend")
				require.NotEqual(t, observerPID, current.pid, "completion must not borrow the held observer backend")
				require.Equal(t, [4]string{"read committed", "1", "250ms", "1500ms"}, current.policy, "every real completion callback must retain the original lifecycle policy")
				require.False(t, current.deadline.IsZero(), "real completion callback must inherit its production unit deadline")
				require.Greater(t, current.remaining, time.Duration(0), "real completion callback must start before its unit deadline")
				require.LessOrEqual(t, current.remaining, db.LifecycleTransactionTimeout, "observation must not extend the original two-second unit budget")
				if len(observed) > 0 {
					require.Equal(t, observed[0].deadline, current.deadline, "all retries must share one original whole-unit deadline")
				}
				observed = append(observed, current)
				probeCtx, probeCancel := context.WithTimeout(ctx, time.Second)
				defer probeCancel()
				tick := time.NewTicker(time.Millisecond)
				defer tick.Stop()
				waiting := false
				for !waiting && probeCtx.Err() == nil {
					var state, waitType, waitEvent, query string
					var blockers []int32
					err = observer.QueryRow(probeCtx, `SELECT state,COALESCE(wait_event_type,''),COALESCE(wait_event,''),pg_blocking_pids(pid),query FROM pg_stat_activity WHERE pid=$1`, current.pid).Scan(&state, &waitType, &waitEvent, &blockers, &query)
					if err != nil {
						break
					}
					waiting = state == "active" && waitType == "Lock" && waitEvent == "advisory" && query == `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`
					if waiting {
						require.Equal(t, []int32{int32(holderPID)}, blockers, "the exact callback contender must be blocked only by the retained PR holder")
						break
					}
					select {
					case <-tick.C:
					case <-probeCtx.Done():
					}
				}
				tick.Stop()
				probeCancel()
				require.NoError(t, err, "observe the exact completion contender through the already warmed observer")
				require.True(t, waiting, "each real callback must wait actively on the production PR advisory query and exact holder PID")
			}
			var result postpublicationPRLockRunResult
			select {
			case result = <-run.returned:
			case <-ctx.Done():
				require.FailNow(t, "the real bounded completion Run must return while its PR holder is still retained", "%v", ctx.Err())
			}
			require.ErrorIs(t, result.err, db.ErrLifecycleContention, "the real runner must report confirmed-aborted exhaustion without a fabricated outcome")
			var contention *db.LifecycleContentionError
			require.ErrorAs(t, result.err, &contention, "the actual kernel must supply its exhausted-attempt classification")
			require.Equal(t, 3, contention.Attempts, "the genuine PR holder must exhaust all three original eligible attempts")
			var codes []string
			for _, actual := range result.attempts {
				var pgErr *pgconn.PgError
				require.ErrorAs(t, actual.err, &pgErr, "each real application callback must return PostgreSQL's actual lock-timeout error")
				codes = append(codes, pgErr.Code)
			}
			require.Equal(t, []string{"55P03", "55P03", "55P03"}, codes, "all three real completion callbacks must abort on the retained PR lock")
			var stillHeld bool
			require.NoError(t, observer.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND granted)`, holderPID).Scan(&stillHeld), "read the genuine holder after Run has already returned")
			require.True(t, stillHeld, "the PR holder must outlive the entire failed real completion unit")
			require.Equal(t, baseline, postpublicationPRLockSnapshot(t, ctx, pool, f), "failed completion must roll back every owner, receipt, metadata, request, reservation, job, provenance and evidence field after the durable publication baseline")
			// Release before returning the unchanged error to the worker: its
			// distinct atomic retirement unit must be able to commit in that leaf.
			require.NoError(t, holder.Rollback(ctx), "release the exact PR holder before worker retry or atomic retirement")
			released = true
			observer.Release()
			observer = nil
			queueStarted := time.Now()
			returnOnce.Do(func() { close(run.returnGate) })
			select {
			case <-done:
				joined = true
			case <-ctx.Done():
				require.FailNow(t, "the actual worker must finish queue handling after holder release", "%v", ctx.Err())
			}
			queueFinished := time.Now()
			afterAttempt := postpublicationPRLockSnapshot(t, ctx, pool, f)
			firstJob := postpublicationPRLockJob(t, ctx, pool, f.job.OrgID, f.jobID)
			for _, field := range []string{"locked_by_node_id", "run_owner_id", "lock_token", "locked_at", "lease_expires_at"} {
				require.JSONEq(t, `null`, string(firstJob[field]), "worker queue settlement must release %s only after its owned write", field)
			}
			require.JSONEq(t, `"worker"`, string(firstJob["owner_kind"]), "queue settlement must retain its original worker ownership kind")
			require.JSONEq(t, fmt.Sprintf("%q", result.err.Error()), string(firstJob["last_error"]), "queue settlement must retain the unchanged real completion error")
			jobChanges := map[string]json.RawMessage{}
			for _, field := range []string{"status", "last_error", "locked_by_node_id", "run_owner_id", "owner_kind", "lock_token", "locked_at", "lease_expires_at", "updated_at"} {
				jobChanges[field] = firstJob[field]
			}
			if tt.exhausted {
				jobChanges["completed_at"] = firstJob["completed_at"]
			} else {
				jobChanges["run_at"] = firstJob["run_at"]
			}
			postpublicationPRLockRow(t, claimedJob, firstJob, jobChanges)
			recoveryJobID := f.jobID
			if tt.exhausted {
				require.JSONEq(t, `"dead_letter"`, string(firstJob["status"]), "first actual claim must atomically retire at its original maximum")
				require.NotEqual(t, json.RawMessage("null"), firstJob["completed_at"], "committed retirement must have a durable terminal timestamp")
				var hook postpublicationPRLockHook
				select {
				case hook = <-hooks:
				default:
					require.FailNow(t, "the first registered probe must observe known-commit retirement before real reconciliation")
				}
				require.NoError(t, hook.err, "first postcommit hook must independently read committed retirement rows")
				require.True(t, hook.committed, "only the actual aware retirement store may release the sealed committed outcome")
				require.Equal(t, claim, hook.claim, "postcommit delivery must retain the original actual claim correlation")
				require.Equal(t, db.CodeReviewTerminalScope{OrgID: f.job.OrgID, JobID: f.jobID, LockToken: claim.token, JobType: models.JobTypeRunCodeReview,
					SessionID: f.job.SessionID, MetadataID: f.job.MetadataID, RepositoryID: f.job.RepositoryID, PullRequestID: f.job.PullRequestID, PolicyID: f.job.PolicyID,
					AssessmentID: f.assessment, HeadSHA: f.job.HeadSHA, OutputKey: f.job.OutputKey, ReviewScope: models.CodeReviewScopeFull}, hook.scope, "the committed outcome must name every original owner field rather than rediscover latest work")
				require.Equal(t, models.JobStatusDeadLetter, hook.jobStatus, "the first hook must see the original job already dead-lettered")
				require.True(t, hook.pending, "the exhausted confirmed controller must expose its exact known-committed completion-recovery disposition")
				require.Equal(t, metadata, hook.metadata, "the approved exception preserves every active metadata field instead of reporting a false failure")
				expectedRetained := published
				expectedRetained.FailureDetail = new("confirmed_completion_recovery_pending")
				require.Equal(t, expectedRetained, hook.assessment, "first retirement may record only its durable recovery-budget diagnostic while preserving the complete confirmed publication and staged outcome")
				require.Equal(t, &f.assessment, hook.schedule.ActiveAssessmentID, "nonterminal confirmed publication must retain its exact assessment reservation at commit")
				require.Equal(t, &f.job.SessionID, hook.schedule.ActiveSessionID, "nonterminal confirmed publication must retain its exact session reservation at commit")
				postpublicationPRLockRow(t, fullAssessmentDispatchObject(t, baseline.assessment), fullAssessmentDispatchObject(t, afterAttempt.assessment), map[string]json.RawMessage{"failure_detail": fullAssessmentDispatchJSON(t, "confirmed_completion_recovery_pending")})
				require.Equal(t, baseline.metadata, afterAttempt.metadata, "neither retirement nor its real hook may falsely fail active confirmed metadata")
				require.Equal(t, baseline.results, afterAttempt.results, "retirement and cleanup must preserve every saved reviewer contribution")
				require.Equal(t, baseline.findings, afterAttempt.findings, "retirement and cleanup must preserve every original inline receipt")
				require.Equal(t, baseline.prompts, afterAttempt.prompts, "retirement and cleanup must preserve original prompt provenance")
				require.Equal(t, baseline.requests, afterAttempt.requests, "nonterminal scheduler cleanup must not settle the joined original request")
				require.Equal(t, baseline.schedule, afterAttempt.schedule, "recovery-pending retirement must not settle or rewrite the protected scheduler reservation")
				require.Equal(t, baseline.guards, afterAttempt.guards, "real retirement cleanup must preserve the full drained execution graph")
				require.Equal(t, baseline.threads, afterAttempt.threads, "real retirement cleanup must not create another reviewer lane")
				require.Equal(t, baseline.messages, afterAttempt.messages, "real retirement cleanup must not create another reviewer message")
				require.Equal(t, baseline.inbox, afterAttempt.inbox, "real retirement cleanup must not create another reviewer inbox delivery")
				require.Equal(t, baseline.stamps, afterAttempt.stamps, "real retirement cleanup must not invent a caller receipt")
				parent, parentErr := f.stores.Sessions.GetByID(ctx, f.job.OrgID, f.job.SessionID)
				require.NoError(t, parentErr, "read the actual parent before confirmed drain recovery")
				require.Equal(t, baselineParent["status"], fullAssessmentDispatchJSON(t, parent.Status), "confirmed completion exhaustion must not fabricate parent failure")
				require.Equal(t, baseline.session, afterAttempt.session, "the approved exception must preserve every original parent field until actual completion")
				require.NoError(t, db.NewCodeReviewScheduleStore(pool).RepairMissingWakes(ctx), "invoke the existing repair entry point for the retained full-controller payload")
				var repairedPayload json.RawMessage
				var repairedDedupe string
				require.NoError(t, pool.QueryRow(ctx, `SELECT id,payload,dedupe_key FROM jobs WHERE org_id=$1 AND job_type='run_code_review' AND status='pending' AND dedupe_key=$2`, f.job.OrgID, "code_review:"+f.job.OutputKey).Scan(&recoveryJobID, &repairedPayload, &repairedDedupe), "repair must create exactly the retained original full-controller identity")
				require.NotEqual(t, f.jobID, recoveryJobID, "repair creates a queue wake rather than reviving the dead-lettered queue row")
				require.Equal(t, originalJob["payload"], repairedPayload, "repair must preserve the full original fork/dispute/policy/head/output provenance")
				require.Equal(t, "code_review:"+f.job.OutputKey, repairedDedupe, "repair must retain the original output dedupe identity")
				repaired := postpublicationPRLockSnapshot(t, ctx, pool, f)
				repaired.jobs = afterAttempt.jobs
				require.Equal(t, afterAttempt, repaired, "explicit wake repair must change only the queue graph, never the retained publication or owner evidence")
			} else {
				require.JSONEq(t, `"pending"`, string(firstJob["status"]), "ordinary failed completion must requeue the original job")
				require.JSONEq(t, `null`, string(firstJob["completed_at"]), "ordinary retry cannot manufacture terminal completion")
				var retryAt time.Time
				require.NoError(t, json.Unmarshal(firstJob["run_at"], &retryAt), "decode the queue's actual ordinary retry schedule")
				require.WithinRange(t, retryAt, queueStarted.Add(retryBackoff(1)), queueFinished.Add(retryBackoff(1)), "ordinary retry must retain the existing attempt-one exponential backoff")
				require.Equal(t, 0, len(hooks), "ordinary retry must deliver no terminal hook")
				comparison := afterAttempt
				comparison.jobs = baseline.jobs
				require.Equal(t, baseline, comparison, "ordinary retry must change only the queue retry fields after complete completion rollback")
				_, err = pool.Exec(ctx, `UPDATE jobs SET run_at=now() WHERE org_id=$1 AND id=$2 AND status='pending'`, f.job.OrgID, f.jobID)
				require.NoError(t, err, "make only the same original pending controller due for its ordinary retry")
			}
			recoveryJobBefore := postpublicationPRLockJob(t, ctx, pool, f.job.OrgID, recoveryJobID)
			captureCalls := capture.calls
			recoveryStarted := time.Now()
			joined = false
			go func() { w.poll(ctx); done <- struct{}{} }()
			select {
			case <-done:
				joined = true
			case <-ctx.Done():
				require.FailNow(t, "unblocked real controller recovery must finish within the case guard", "%v", ctx.Err())
			}
			recoveryFinished := time.Now()
			var recoveryClaim postpublicationPRLockClaim
			select {
			case recoveryClaim = <-claims:
			default:
				require.FailNow(t, "actual Worker.poll must claim the original or repaired retained controller")
			}
			require.Equal(t, recoveryJobID, recoveryClaim.jobID, "recovery must process the expected retained queue wake")
			require.NotEqual(t, claim.token, recoveryClaim.token, "recovery must acquire a fresh actual queue lease")
			require.Equal(t, captureCalls, capture.calls, "confirmed receipt recovery must use the shortcut without recapturing inputs")
			requests, fileRequests = publisher.submitted()
			require.Equal(t, wantRequests, requests, "both full-handler attempts together must make exactly one original logical SubmitReview call")
			require.Equal(t, []codereviewsvc.PullRequestFilesRequest{{InstallationID: 1, Repository: "test/saved-review", PullNumber: 9}}, fileRequests, "confirmed recovery must not fetch or rebuild a different diff")
			completed, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read the exact original assessment after unblocked real completion")
			require.NotNil(t, completed.CompletedAt, "real completion must retain a durable commit timestamp")
			expectedCompleted := published
			expectedCompleted.Status, expectedCompleted.CompletedAt = models.CodeReviewAssessmentCompleted, completed.CompletedAt
			require.Equal(t, expectedCompleted, completed, "recovery must preserve the complete original body, manifest, key, outcome, provenance and confirmed publication facts")
			completedMetadata, err := f.stores.CodeReviews.GetBySessionID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read exact completed metadata rather than latest-session state")
			require.Equal(t, models.CodeReviewSessionStatusCompleted, completedMetadata.Status, "metadata must complete with the original confirmed assessment")
			require.Equal(t, &body, completedMetadata.FinalReviewBody, "recovery must retain the original submitted body")
			require.True(t, confirmedFullAssessmentReceiptMatches(published, completedMetadata), "completed metadata must preserve the original confirmed receipt identity")
			final := postpublicationPRLockSnapshot(t, ctx, pool, f)
			finalMetadata := fullAssessmentDispatchObject(t, final.metadata)
			require.Equal(t, fullAssessmentDispatchJSON(t, f.assessment), finalMetadata["assessment_id"], "evidence linking must pin the complete metadata row to the exact original assessment")
			var metadataCompletedAt time.Time
			require.NoError(t, json.Unmarshal(finalMetadata["completed_at"], &metadataCompletedAt), "decode the actual completion commit's metadata timestamp")
			require.WithinRange(t, metadataCompletedAt, recoveryStarted, recoveryFinished, "metadata completion must belong to the actual unblocked recovery attempt")
			postpublicationPRLockRow(t, fullAssessmentDispatchObject(t, baseline.metadata), finalMetadata, map[string]json.RawMessage{
				"status": json.RawMessage(`"completed"`), "phase": json.RawMessage(`null`), "decision": json.RawMessage(`"needs_human_review"`),
				"acceptable": json.RawMessage(`false`), "risk_reason_details": json.RawMessage(`[]`),
				"assessment_id": fullAssessmentDispatchJSON(t, f.assessment), "completed_at": finalMetadata["completed_at"],
			})
			finalSchedule := fullAssessmentDispatchObject(t, final.schedule)
			postpublicationPRLockRow(t, fullAssessmentDispatchObject(t, baseline.schedule), finalSchedule, map[string]json.RawMessage{
				"active_assessment_id": json.RawMessage(`null`), "active_session_id": json.RawMessage(`null`),
				"current_assessment_id": fullAssessmentDispatchJSON(t, f.assessment), "state": json.RawMessage(`"covered"`), "updated_at": finalSchedule["updated_at"],
			})
			finalParent := fullAssessmentDispatchObject(t, final.session)
			postpublicationPRLockRow(t, baselineParent, finalParent, map[string]json.RawMessage{
				"status": json.RawMessage(`"completed"`), "code_review_owner_pr_id": fullAssessmentDispatchJSON(t, f.job.PullRequestID),
				"completed_at": finalParent["completed_at"], "last_activity_at": finalParent["last_activity_at"], "error": json.RawMessage(`null`),
				"failure_explanation": json.RawMessage(`null`), "failure_category": json.RawMessage(`null`), "failure_next_steps": json.RawMessage(`null`),
				"failure_retry_advised": json.RawMessage(`false`),
			})
			for _, evidence := range []struct{ before, after json.RawMessage }{{baseline.results, final.results}, {baseline.findings, final.findings}, {baseline.prompts, final.prompts}} {
				var beforeRows, afterRows []map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(evidence.before, &beforeRows), "decode full retained evidence rows before attribution")
				require.NoError(t, json.Unmarshal(evidence.after, &afterRows), "decode full retained evidence rows after attribution")
				require.NotEmpty(t, beforeRows, "positive retained evidence fixture must prevent a vacuous attribution assertion")
				for _, row := range beforeRows {
					row["assessment_id"] = fullAssessmentDispatchJSON(t, f.assessment)
				}
				require.Equal(t, beforeRows, afterRows, "completion may only attribute each entire original evidence row to its exact original assessment")
			}
			var beforeRequests, finalRequests []map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(baseline.requests, &beforeRequests), "decode complete original joined request rows")
			require.NoError(t, json.Unmarshal(final.requests, &finalRequests), "decode complete original settled request rows")
			var requestIDs []json.RawMessage
			for _, row := range beforeRequests {
				requestIDs = append(requestIDs, row["id"])
			}
			require.Equal(t, []json.RawMessage{fullAssessmentDispatchJSON(t, requestID)}, requestIDs, "positive request fixture must name exactly the complete original durable request")
			beforeRequests[0]["status"] = json.RawMessage(`"satisfied"`)
			require.Equal(t, beforeRequests, finalRequests, "only successful exact completion may satisfy the complete original request row")
			schedule, err := db.NewCodeReviewScheduleStore(pool).Get(ctx, f.job.OrgID, f.job.PullRequestID)
			require.NoError(t, err, "read final guarded scheduler settlement")
			require.Equal(t, &f.assessment, schedule.CurrentAssessmentID, "successful recovery must advance exactly the original assessment")
			require.Nil(t, schedule.ActiveAssessmentID, "successful recovery must release the original assessment reservation")
			require.Nil(t, schedule.ActiveSessionID, "successful recovery must release the original session reservation")
			require.Equal(t, models.CodeReviewScheduleCovered, schedule.State, "the original matching head may become covered only after completion")
			var parentStatus models.SessionStatus
			var owner *uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT status,code_review_owner_pr_id FROM sessions WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID).Scan(&parentStatus, &owner), "read parent recovery and exact full-assessment conversation owner")
			require.Equal(t, models.SessionStatusCompleted, parentStatus, "confirmed full recovery must complete the original drained parent")
			require.Equal(t, &f.job.PullRequestID, owner, "confirmed full completion must retain the exact original PR owner")
			finalJob := postpublicationPRLockJob(t, ctx, pool, f.job.OrgID, recoveryJobID)
			require.JSONEq(t, `"succeeded"`, string(finalJob["status"]), "the actual original or repaired queue wake must succeed")
			wantAttempts := 2
			if tt.exhausted {
				wantAttempts = 1
			}
			require.Equal(t, fullAssessmentDispatchJSON(t, wantAttempts), finalJob["attempts"], "only a real fresh claim may consume the expected original or repaired attempt")
			for _, field := range []string{"locked_by_node_id", "run_owner_id", "lock_token", "locked_at", "lease_expires_at"} {
				require.JSONEq(t, `null`, string(finalJob[field]), "successful queue settlement must release %s", field)
			}
			var jobCompletedAt time.Time
			require.NoError(t, json.Unmarshal(finalJob["completed_at"], &jobCompletedAt), "decode the real succeeded queue timestamp")
			require.WithinRange(t, jobCompletedAt, recoveryStarted, recoveryFinished, "succeeded queue completion must belong to the actual recovery attempt")
			postpublicationPRLockRow(t, recoveryJobBefore, finalJob, map[string]json.RawMessage{
				"status": json.RawMessage(`"succeeded"`), "attempts": fullAssessmentDispatchJSON(t, wantAttempts),
				"locked_by_node_id": json.RawMessage(`null`), "run_owner_id": json.RawMessage(`null`), "lock_token": json.RawMessage(`null`),
				"locked_at": json.RawMessage(`null`), "lease_expires_at": json.RawMessage(`null`),
				"updated_at": finalJob["updated_at"], "completed_at": finalJob["completed_at"],
			})
			if tt.exhausted {
				require.Equal(t, firstJob, postpublicationPRLockJob(t, ctx, pool, f.job.OrgID, f.jobID), "successful repaired wake must preserve every field of the original dead-lettered job")
			}
			require.Equal(t, originalJob["payload"], finalJob["payload"], "the succeeded controller must preserve every original payload field")
			require.Equal(t, originalJob["dedupe_key"], finalJob["dedupe_key"], "the succeeded controller must retain its original dedupe key")
			var identities []uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT ARRAY(SELECT id FROM code_review_revision_assessments WHERE org_id=$1 ORDER BY id)`, f.job.OrgID).Scan(&identities), "enumerate exact assessment identities after all real recovery paths")
			require.Equal(t, []uuid.UUID{f.assessment}, identities, "no recovery path may create a replacement review assessment")
			require.NoError(t, pool.QueryRow(ctx, `SELECT ARRAY(SELECT id FROM sessions WHERE org_id=$1 ORDER BY id)`, f.job.OrgID).Scan(&identities), "enumerate exact session identities after all real recovery paths")
			require.Equal(t, []uuid.UUID{f.job.SessionID}, identities, "no recovery path may create a replacement review session")
			require.NoError(t, pool.QueryRow(ctx, `SELECT ARRAY(SELECT id FROM jobs WHERE org_id=$1 ORDER BY created_at,id)`, f.job.OrgID).Scan(&identities), "enumerate every durable wake after actual recovery")
			wantJobs := []uuid.UUID{f.jobID}
			if tt.exhausted {
				wantJobs = append(wantJobs, recoveryJobID)
			}
			require.Equal(t, wantJobs, identities, "only exhaustion may create the single explicitly repaired wake for the original review")
			require.Equal(t, baseline.guards, final.guards, "recovery must not create new execution or runtime ownership")
			require.Equal(t, baseline.threads, final.threads, "recovery must not create replacement reviewer lanes")
			require.Equal(t, baseline.messages, final.messages, "recovery must not send another reviewer command")
			require.Equal(t, baseline.inbox, final.inbox, "recovery must not create another inbox delivery")
			require.Equal(t, baseline.stamps, final.stamps, "recovery must not invent a new caller receipt")
			require.Equal(t, 0, len(hooks), "successful recovery must not deliver an extra terminal hook")
		})
	}
}
