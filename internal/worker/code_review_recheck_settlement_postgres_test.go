package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// These cases invoke the actual publication-lock owner, not a replacement
// callback. GitHub work uses local fakes; the full unchanged schema supplies the
// assessment, request, scheduler, queue and receipt constraints.
func TestRecheckPublicationSettlementFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	pool := newRecheckPublicationSettlementPool(t)
	_, err := pool.Exec(context.Background(), `
CREATE TABLE synthetic_recheck_publication_rejections(org_id uuid PRIMARY KEY,assessment_id uuid NOT NULL,request_id uuid NOT NULL);
CREATE FUNCTION synthetic_recheck_publication_reject() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target synthetic_recheck_publication_rejections;
BEGIN
 IF NEW.job_type <> 'sync_code_review_status_comment' THEN RETURN NEW; END IF;
 SELECT * INTO target FROM synthetic_recheck_publication_rejections WHERE org_id=NEW.org_id;
 IF NOT FOUND THEN RETURN NEW; END IF;
 IF NOT EXISTS(SELECT 1 FROM code_review_revision_assessments a WHERE a.org_id=NEW.org_id AND a.id=target.assessment_id AND a.status='completed' AND a.publication_state='confirmed' AND a.publication_receipt IS NOT NULL AND a.github_review_id=77)
 OR NOT EXISTS(SELECT 1 FROM code_review_requests r WHERE r.org_id=NEW.org_id AND r.id=target.request_id AND r.assessment_id=target.assessment_id AND r.status='satisfied')
 OR NOT EXISTS(SELECT 1 FROM code_review_pr_state s WHERE s.org_id=NEW.org_id AND s.pull_request_id=(NEW.payload->>'pull_request_id')::uuid AND s.active_assessment_id IS NULL AND s.active_session_id IS NULL AND s.current_assessment_id=target.assessment_id AND s.state='covered')
 THEN RAISE EXCEPTION 'synthetic publication rejection missed earlier receipt and settlement writes'; END IF;
 RAISE EXCEPTION 'synthetic late recheck status-comment enqueue failure';
END $$;
CREATE TRIGGER synthetic_recheck_publication_reject BEFORE INSERT ON jobs FOR EACH ROW EXECUTE FUNCTION synthetic_recheck_publication_reject();`)
	require.NoError(t, err, "install a fixture-only final job failure that first verifies genuine receipt and settlement changes")
	tests := []struct {
		name            string
		reconcile       bool
		noStatusUpdater bool
		lateJobFailure  bool
		commitFailure   bool
		wrongLease      bool
	}{
		{name: "confirmed receipt commit"},
		{name: "confirmed receipt late job failure", lateJobFailure: true},
		{name: "confirmed receipt commit failure", commitFailure: true},
		{name: "confirmed receipt without status updater", noStatusUpdater: true},
		{name: "reconciled receipt commit", reconcile: true},
		{name: "reconciled receipt late job failure", reconcile: true, lateJobFailure: true},
		{name: "lost supervisor lease", wrongLease: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			fixture, baseline := seedDeadLetterRecheck(t, pool)
			requestID := uuid.New()
			publication := models.CodeReviewPublicationConfirmed
			if tt.reconcile {
				publication = models.CodeReviewPublicationUncertain
			}
			_, err := pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status='publishing',publication_state=$3,submitted_commit_sha=head_sha,publication_receipt=CASE WHEN $3='confirmed' THEN '{"synthetic":"retained receipt"}'::jsonb ELSE NULL END,github_review_id=CASE WHEN $3='confirmed' THEN 77 ELSE NULL END,github_review_url=CASE WHEN $3='confirmed' THEN 'https://example.invalid/review/77' ELSE NULL END WHERE org_id=$1 AND id=$2`, fixture.job.OrgID, fixture.assessment, publication)
			require.NoError(t, err, "stage the exact saved evidence outcome with either an immutable confirmation or a prior uncertain send")
			_, err = pool.Exec(ctx, `INSERT INTO code_review_requests(id,org_id,repository_id,pull_request_id,source_kind,source_identity,mode,input_hash,target_generation,session_id,assessment_id,status) VALUES($1,$2,$3,$4,'synthetic_publication',$1::uuid::text,'ensure_current','recheck-input',2,$5,$6,'joined')`, requestID, fixture.job.OrgID, fixture.job.RepositoryID, fixture.job.PullRequestID, fixture.job.SessionID, fixture.assessment)
			require.NoError(t, err, "join a real durable request to exactly the evidence assessment being published")
			if tt.lateJobFailure {
				_, err := pool.Exec(ctx, `INSERT INTO synthetic_recheck_publication_rejections VALUES($1,$2,$3)`, fixture.job.OrgID, fixture.assessment, requestID)
				require.NoError(t, err, "scope the final queue rejection to this generated tenant and exact assessment/request pair")
			}
			assessment, err := fixture.stores.CodeReviewAssessments.GetByID(ctx, fixture.job.OrgID, fixture.assessment)
			require.NoError(t, err, "load the unchanged staged result and original receipt before publication")
			pr, err := fixture.stores.PullRequests.GetByID(ctx, fixture.job.OrgID, fixture.job.PullRequestID)
			require.NoError(t, err, "load the actual owned PR used by the publication function")
			completion := models.CodeReviewAssessmentCompletion{ResultOrigin: *assessment.ResultOrigin, CoverageComplete: assessment.CoverageComplete, Decision: *assessment.Decision, Acceptable: *assessment.Acceptable, RiskReasonDetails: assessment.RiskReasonDetails, StructuredOutcome: assessment.StructuredOutcome, RenderedBody: *assessment.RenderedBody}
			// Pre-open this observer. Neither commit nor the final queue insertion
			// borrows the shared setup/DDL pool while holding the publication owner.
			observer, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig.Copy())
			require.True(t, err == nil, "open a physical observer on only the generated fixture without printing its DSN")
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				require.NoError(t, observer.Close(cleanup), "close the dedicated observer independently of the operation context")
			})
			configuration := pool.Config()
			configuration.MaxConns, configuration.MinConns = 1, 0
			callerPool, err := pgxpool.NewWithConfig(ctx, configuration)
			require.True(t, err == nil, "open a single-connection pool only for the actual publication transaction owner")
			t.Cleanup(callerPool.Close)
			owner := &recheckPublicationOwner{Pool: callerPool, observer: observer, orgID: assessment.OrgID, failCommit: tt.commitFailure}
			fixture.stores.CodeReviews = db.NewCodeReviewStore(owner)
			publisher := &recheckSettlementPublisher{}
			services := &Services{CodeReviews: publisher}
			if tt.noStatusUpdater {
				services.CodeReviews = &publisher.fakeRecheckPublisher
			}
			capture := &fixedRecheckCapture{captureErr: errors.New("synthetic freshness unavailable; reconcile saved receipt instead")}
			services.CodeReviewInputCapture = capture
			callCtx := jobctx.WithJobID(jobctx.WithLockToken(ctx, fixture.token), fixture.jobID)
			if tt.wrongLease {
				callCtx = jobctx.WithLockToken(callCtx, uuid.New())
			}
			err = publishCodeReviewRecheck(callCtx, fixture.stores, services, assessment, pr, completion.Decision, completion.RenderedBody, completion)
			require.NoError(t, owner.observationErr, "all owner observations must use the preopened separate physical connection")
			require.Equal(t, 1, owner.begins, "the actual publication-lock owner must acquire exactly one outer transaction")
			require.Zero(t, owner.tx.nestedBegins, "the publication callback must never begin a settlement savepoint or replacement transaction")
			require.Equal(t, 1, owner.tx.rollbackCalls, "the unchanged publication owner must retain its deferred cleanup on every path")
			after, readErr := recheckPublicationGraph(ctx, observer, assessment.OrgID)
			require.NoError(t, readErr, "read every assessment, receipt, request, scheduler and job column after the actual owner returns")
			require.Empty(t, publisher.requests, "confirmed or reconciled receipts must never cause this proof to submit an external review")
			require.Empty(t, publisher.statusCalls, "completion queues status synchronization without performing an external comment write")
			wantCaptures, wantLookups := 0, 0
			if tt.reconcile {
				wantCaptures, wantLookups = 1, 1
			}
			require.Equal(t, wantCaptures, capture.calls, "only uncertain publication may consult the existing freshness/reconciliation protocol")
			require.Equal(t, wantLookups, len(publisher.reconciliations), "a preexisting confirmed receipt must not be looked up again")
			if tt.wrongLease {
				require.ErrorIs(t, err, pgx.ErrNoRows, "a lost supervisor lease must fail before any assessment or scheduler write")
				require.Equal(t, owner.before, after, "a lost lease must preserve the whole publication graph exactly")
				require.Zero(t, owner.tx.commitCalls, "a rejected lease must not reach the outer commit")
				return
			}
			if !tt.noStatusUpdater {
				expected := expectedRecheckPublicationGraph(t, owner.before, assessment, requestID, owner.timestamp, tt.reconcile, false, nil)
				require.JSONEq(t, string(expected), string(owner.beforeJob), "the final queue insertion must follow exactly the real receipt, assessment, request and scheduler mutations and no unrelated row change")
			}
			if tt.lateJobFailure || tt.commitFailure {
				if tt.lateJobFailure {
					require.ErrorContains(t, err, "synthetic late recheck status-comment enqueue failure", "the final job statement may fail only after the server verifies earlier receipt and settlement writes")
					require.Zero(t, owner.tx.commitCalls, "a final job SQL error must not commit any earlier settlement")
				} else {
					require.ErrorIs(t, err, errRecheckPublicationCommit, "an owner commit error must propagate without a callback retry or replacement owner")
					require.Equal(t, 1, owner.tx.commitCalls, "an injected commit failure must occur only after genuine callback success")
					require.NotEmpty(t, owner.staged, "commit rejection must follow a fully staged real publication graph")
				}
				require.Equal(t, owner.before, after, "the actual owner must roll back every receipt, assessment, joined request, scheduler and queue column after a late failure")
				return
			}
			require.NoError(t, err, "the actual publication owner must compose settlement while occupying its pool's only connection")
			require.Equal(t, 1, owner.tx.commitCalls, "only the outer publication owner may commit the complete unit")
			require.Equal(t, owner.staged, after, "the real commit must expose exactly the graph staged on the original transaction")
			expected := expectedRecheckPublicationGraph(t, owner.before, assessment, requestID, owner.timestamp, tt.reconcile, !tt.noStatusUpdater, owner.newJob)
			require.JSONEq(t, string(expected), string(after), "successful completion must change only the exact receipt, terminal clocks, joined request, scheduler pointers and optional status job")
			unchangedBaseline, err := fixture.stores.CodeReviewAssessments.GetByID(ctx, baseline.OrgID, baseline.ID)
			require.NoError(t, err, "reload the original full baseline after evidence publication")
			require.Equal(t, baseline, unchangedBaseline, "evidence publication must preserve every immutable baseline field")
			// Replaying the saved input takes the existing completed branch. It
			// neither resettles the scheduler nor creates another status job.
			require.NoError(t, publishCodeReviewRecheck(callCtx, fixture.stores, services, assessment, pr, completion.Decision, completion.RenderedBody, completion), "a committed publication replay must remain a no-op")
			replayed, err := recheckPublicationGraph(ctx, observer, assessment.OrgID)
			require.NoError(t, err, "read the exact graph after replay")
			require.Equal(t, after, replayed, "completed publication replay must preserve all durable rows and clocks exactly")
		})
	}
}

type recheckSettlementPublisher struct {
	fakeRecheckPublisher
	statusCalls []string
}

func (p *recheckSettlementPublisher) ReconcileAssessmentPublication(_ context.Context, request codereviewsvc.SubmitReviewRequest) (codereviewsvc.SubmitReviewResult, bool, error) {
	p.reconciliations = append(p.reconciliations, request)
	return codereviewsvc.SubmitReviewResult{ID: 77, URL: "https://example.invalid/review/77", SubmittedCommitSHA: request.HeadSHA}, true, nil
}

func (p *recheckSettlementPublisher) UpsertReviewStatusComment(context.Context, codereviewsvc.UpsertReviewStatusCommentRequest) (int64, error) {
	p.statusCalls = append(p.statusCalls, "upsert")
	return 0, errors.New("synthetic completion must queue status synchronization rather than call GitHub")
}

func (p *recheckSettlementPublisher) HideReviewSummary(context.Context, codereviewsvc.HideReviewSummaryRequest) error {
	p.statusCalls = append(p.statusCalls, "hide")
	return errors.New("synthetic completion must not hide an external review")
}

var errRecheckPublicationCommit = errors.New("synthetic recheck publication commit failure")

type recheckPublicationOwner struct {
	*pgxpool.Pool
	observer          *pgx.Conn
	orgID             uuid.UUID
	failCommit        bool
	begins            int
	tx                *recheckPublicationTx
	before, beforeJob json.RawMessage
	staged, timestamp json.RawMessage
	newJob            map[string]any
	observationErr    error
}

func (s *recheckPublicationOwner) Begin(ctx context.Context) (pgx.Tx, error) {
	s.begins++
	before, err := recheckPublicationGraph(ctx, s.observer, s.orgID)
	if err != nil {
		s.observationErr = err
		return nil, err
	}
	s.before = before
	owner, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	s.tx = &recheckPublicationTx{Tx: owner, owner: s}
	if s.observer.PgConn().PID() == owner.Conn().PgConn().PID() {
		s.observationErr = errors.New("publication observer must use a different physical connection")
		if rollbackErr := owner.Rollback(ctx); rollbackErr != nil {
			return nil, errors.Join(s.observationErr, rollbackErr)
		}
		return nil, s.observationErr
	}
	if err := owner.QueryRow(ctx, "SELECT to_jsonb(transaction_timestamp())").Scan(&s.timestamp); err != nil {
		s.observationErr = err
		if rollbackErr := owner.Rollback(ctx); rollbackErr != nil {
			return nil, errors.Join(err, rollbackErr)
		}
		return nil, err
	}
	return s.tx, nil
}

type recheckPublicationTx struct {
	pgx.Tx
	owner                                    *recheckPublicationOwner
	nestedBegins, commitCalls, rollbackCalls int
}

func (tx *recheckPublicationTx) Begin(context.Context) (pgx.Tx, error) {
	tx.nestedBegins++
	return nil, errors.New("nested recheck publication settlement transaction is forbidden")
}

func (tx *recheckPublicationTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	if strings.Contains(query, "INSERT INTO jobs") && len(args) == 1 {
		if named, ok := args[0].(pgx.NamedArgs); ok && named["job_type"] == models.JobTypeSyncCodeReviewStatusComment {
			staged, err := recheckPublicationGraph(ctx, tx.Tx, tx.owner.orgID)
			if err == nil {
				tx.owner.beforeJob = staged
				err = tx.owner.checkInvisible(ctx)
			}
			if err != nil {
				tx.owner.observationErr = err
				return recheckPublicationErrorRow{err: err}
			}
		}
	}
	return tx.Tx.QueryRow(ctx, query, args...)
}

func (tx *recheckPublicationTx) Commit(ctx context.Context) error {
	tx.commitCalls++
	staged, err := recheckPublicationGraph(ctx, tx.Tx, tx.owner.orgID)
	if err == nil {
		tx.owner.staged = staged
		err = tx.owner.checkInvisible(ctx)
	}
	if err != nil {
		tx.owner.observationErr = err
		return err
	}
	var graph map[string][]map[string]any
	if err := json.Unmarshal(staged, &graph); err != nil {
		tx.owner.observationErr = err
		return err
	}
	for _, job := range graph["jobs"] {
		if job["job_type"] == models.JobTypeSyncCodeReviewStatusComment {
			tx.owner.newJob = job
		}
	}
	if tx.owner.failCommit {
		// This is a deterministic pre-COMMIT rejection, not an ambiguous
		// network-outcome simulation. The real owner's defer then rolls back.
		return errRecheckPublicationCommit
	}
	return tx.Tx.Commit(ctx)
}

func (tx *recheckPublicationTx) Rollback(ctx context.Context) error {
	tx.rollbackCalls++
	return tx.Tx.Rollback(ctx)
}

func (s *recheckPublicationOwner) checkInvisible(ctx context.Context) error {
	visible, err := recheckPublicationGraph(ctx, s.observer, s.orgID)
	if err != nil {
		return err
	}
	if string(visible) != string(s.before) {
		return errors.New("publication settlement became visible before its outer commit")
	}
	return nil
}

type recheckPublicationErrorRow struct{ err error }

func (r recheckPublicationErrorRow) Scan(...any) error { return r.err }

func recheckPublicationGraph(ctx context.Context, conn db.DBTX, orgID uuid.UUID) (json.RawMessage, error) {
	var graph json.RawMessage
	err := conn.QueryRow(ctx, `SELECT jsonb_build_object(
 'sessions',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM sessions s WHERE org_id=$1),
 'threads',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM session_threads s WHERE org_id=$1),
 'metadata',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM code_review_session_metadata s WHERE org_id=$1),
 'results',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM code_review_agent_results s WHERE org_id=$1),
 'assessments',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM code_review_revision_assessments s WHERE org_id=$1),
 'requests',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM code_review_requests s WHERE org_id=$1),
 'schedule',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM code_review_pr_state s WHERE org_id=$1),
 'jobs',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM jobs s WHERE org_id=$1))`, orgID).Scan(&graph)
	return graph, err
}

func expectedRecheckPublicationGraph(t *testing.T, before json.RawMessage, assessment models.CodeReviewAssessment, requestID uuid.UUID, timestamp json.RawMessage, reconciled, withJob bool, actualJob map[string]any) json.RawMessage {
	t.Helper()
	var graph map[string][]map[string]any
	var stamp string
	require.NoError(t, json.Unmarshal(before, &graph), "decode all original row columns for an exact expected graph")
	require.NoError(t, json.Unmarshal(timestamp, &stamp), "use the actual outer transaction clock for all unchanged now() semantics")
	for _, row := range graph["assessments"] {
		if row["id"] != assessment.ID.String() {
			continue
		}
		row["status"], row["completed_at"] = "completed", stamp
		if reconciled {
			row["publication_state"], row["failure_detail"] = "confirmed", nil
			row["github_review_id"], row["github_review_url"] = float64(77), "https://example.invalid/review/77"
			row["publication_receipt"] = map[string]any{"OutputKey": assessment.PublicationKey, "CommitSHA": assessment.HeadSHA, "InputDigest": assessment.InputDigest, "SummaryID": float64(77), "SummaryURL": "https://example.invalid/review/77", "FormalApprovalID": nil, "FormalApprovalURL": nil}
		}
	}
	for _, row := range graph["requests"] {
		if row["id"] == requestID.String() {
			row["status"] = "satisfied"
		}
	}
	for _, row := range graph["schedule"] {
		row["active_assessment_id"], row["active_session_id"] = nil, nil
		row["current_assessment_id"], row["state"], row["updated_at"] = assessment.ID.String(), "covered", stamp
	}
	if withJob {
		require.NotNil(t, actualJob, "successful status synchronization must have one actual generated job identity")
		id, err := uuid.Parse(actualJob["id"].(string))
		require.NoError(t, err, "the only unconstrained new job field must be a generated UUID")
		require.NotEqual(t, uuid.Nil, id, "the generated status job must have a real nonzero identity")
		job := map[string]any{
			"id": id.String(), "org_id": assessment.OrgID.String(), "queue": "default", "job_type": models.JobTypeSyncCodeReviewStatusComment,
			"payload":  map[string]any{"org_id": assessment.OrgID.String(), "session_id": assessment.SessionID.String(), "repository_id": assessment.RepositoryID.String(), "pull_request_id": assessment.PullRequestID.String()},
			"priority": float64(3), "status": "pending", "attempts": float64(0), "max_attempts": float64(codeReviewStatusCommentJobMaxAttempts), "run_at": stamp,
			"locked_by_node_id": nil, "locked_at": nil, "last_error": nil, "dedupe_key": "code_review_status_comment:assessment:" + assessment.ID.String(),
			"created_at": stamp, "updated_at": stamp, "completed_at": nil, "lease_expires_at": nil, "lock_token": nil, "run_owner_id": nil, "target_node_id": nil,
			"owner_kind": "worker", "retry_window_started_at": nil,
		}
		require.Equal(t, job, actualJob, "every status-job column must retain the existing queue, payload, dedupe, priority, retry and default contracts")
		graph["jobs"] = append(graph["jobs"], job)
		sort.Slice(graph["jobs"], func(i, j int) bool { return graph["jobs"][i]["id"].(string) < graph["jobs"][j]["id"].(string) })
	}
	expected, err := json.Marshal(graph)
	require.NoError(t, err, "encode the full exact expected graph without discarding unrelated row fields")
	return expected
}

func newRecheckPublicationSettlementPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL 15+ for actual recheck publication settlement proof")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := pgx.Connect(ctx, raw)
	require.True(t, err == nil, "connect only to the supplied disposable server without printing its DSN")
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		require.NoError(t, admin.Close(cleanup), "close the generated fixture administrator connection")
	})
	var version int
	require.NoError(t, admin.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version), "verify the full unchanged schema has a supported PostgreSQL version")
	require.GreaterOrEqual(t, version, 150000, "actual publication settlement proof requires PostgreSQL 15 or newer")
	name := "recheck_publication_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{name}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE DATABASE "+identifier)
	require.NoError(t, err, "create only this independently generated synthetic database")
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, err := admin.Exec(cleanup, "DROP DATABASE "+identifier)
		require.NoError(t, err, "drop only this generated database after owned connections close, without forced termination")
	})
	target, err := url.Parse(raw)
	require.True(t, err == nil, "parse the fixture target without publishing connection details")
	query := target.Query()
	for key := range query {
		if strings.EqualFold(key, "dbname") || strings.EqualFold(key, "database") {
			query.Del(key)
		}
	}
	target.Path, target.RawPath, target.RawQuery = "/"+name, "", query.Encode()
	configuration, err := pgxpool.ParseConfig(target.String())
	require.True(t, err == nil && configuration.ConnConfig.Database == name, "both database drivers must target only this generated fixture")
	configuration.MaxConns, configuration.MinConns = 2, 0
	source, err := filepath.Abs("../../migrations")
	require.NoError(t, err, "resolve the complete unchanged migration chain")
	migration, err := migrate.New("file://"+source, target.String())
	require.True(t, err == nil, "open explicitly registered migration drivers only for this generated database")
	migrationErr := migration.Up()
	sourceErr, databaseErr := migration.Close()
	require.NoError(t, migrationErr, "apply the full real schema before invoking the actual publication callback")
	require.NoError(t, sourceErr, "close the migration source before parallel owners start")
	require.NoError(t, databaseErr, "close the migration database connection before parallel owners start")
	pool, err := pgxpool.NewWithConfig(ctx, configuration)
	require.True(t, err == nil, "open the generated fixture's bounded setup pool")
	t.Cleanup(pool.Close)
	return pool
}
