package db

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func newSchedulingPostgres(t *testing.T) (*pgxpool.Pool, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL for scheduling concurrency proof")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	require.NoError(t, err, "connect disposable PostgreSQL")
	schema := "review_schedule_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "create isolated test schema")
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err, "remove isolated schema")
		require.NoError(t, admin.Close(ctx), "close database connection")
	})
	cfg, err := pgxpool.ParseConfig(url)
	require.NoError(t, err, "parse pool settings")
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err, "create concurrent pool")
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `CREATE TABLE organizations(id uuid PRIMARY KEY);CREATE TABLE repositories(id uuid PRIMARY KEY,org_id uuid,full_name text);CREATE TABLE pull_requests(id uuid PRIMARY KEY,org_id uuid,github_repo text,title text,github_pr_url text,github_pr_number integer);CREATE TABLE users(id uuid PRIMARY KEY);CREATE TABLE sessions(id uuid PRIMARY KEY,org_id uuid,code_review_owner_pr_id uuid,status text,container_id text,turn_holding_container boolean);CREATE TABLE code_review_policies(id uuid PRIMARY KEY);
 CREATE TABLE code_review_session_metadata(org_id uuid,session_id uuid,pull_request_id uuid,status text,created_at timestamptz DEFAULT now(),review_output_key text,id uuid DEFAULT gen_random_uuid());
 CREATE TABLE session_threads(org_id uuid,session_id uuid,status text,id uuid DEFAULT gen_random_uuid(),cancel_requested_at timestamptz,completed_at timestamptz,last_activity_at timestamptz);
 CREATE TABLE code_review_revision_assessments(id uuid PRIMARY KEY,org_id uuid,repository_id uuid,pull_request_id uuid,session_id uuid,review_scope text,status text,head_sha text DEFAULT '',base_sha text DEFAULT '',base_ref text DEFAULT '',generation bigint DEFAULT 1,superseded_by_assessment_id uuid,failure_detail text,created_at timestamptz DEFAULT now(),publication_state text DEFAULT 'not_started',result_origin text,publication_key text DEFAULT '',metadata_id uuid,publication_receipt jsonb,github_review_id bigint,completed_at timestamptz,superseded_at timestamptz);
 CREATE TABLE code_review_recheck_dispatches(org_id uuid,session_id uuid,status text,id uuid DEFAULT gen_random_uuid(),assessment_id uuid,thread_id uuid,job_id uuid,created_at timestamptz DEFAULT now());
 CREATE TABLE thread_runtimes(org_id uuid,session_id uuid,status text);
 CREATE TABLE session_executors(org_id uuid,session_id uuid,status text,thread_id uuid,job_id uuid);
 CREATE TABLE jobs(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),org_id uuid,queue text,job_type text,payload jsonb,priority int,dedupe_key text,status text DEFAULT 'pending',run_at timestamptz DEFAULT now(),created_at timestamptz DEFAULT now(),updated_at timestamptz DEFAULT now(),attempts int DEFAULT 0,max_attempts int DEFAULT 8,last_error text,locked_by_node_id text,run_owner_id text,owner_kind text,lock_token uuid,locked_at timestamptz,lease_expires_at timestamptz,completed_at timestamptz);
 CREATE UNIQUE INDEX jobs_dedupe ON jobs(queue,dedupe_key) WHERE status IN ('pending','running');`)
	require.NoError(t, err, "create scheduling dependencies")
	up, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000289_code_review_scheduling.up.sql"))
	require.NoError(t, err, "read actual migration")
	_, err = pool.Exec(ctx, string(up))
	require.NoError(t, err, "apply scheduling migration")
	_, err = pool.Exec(ctx, `ALTER TABLE code_review_pr_state ADD COLUMN active_assessment_id uuid, ADD COLUMN current_assessment_id uuid; ALTER TABLE code_review_requests ADD COLUMN assessment_id uuid`)
	require.NoError(t, err, "add continuation columns to scheduling fixture")
	orgID, repoID, prID := uuid.New(), uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO organizations VALUES($1)`, orgID)
	require.NoError(t, err, "seed org")
	_, err = pool.Exec(ctx, `INSERT INTO repositories VALUES($1,$2,'test/repo')`, repoID, orgID)
	require.NoError(t, err, "seed repository")
	_, err = pool.Exec(ctx, `INSERT INTO pull_requests VALUES($1,$2,'test/repo','Change','https://github.com/test/repo/pull/1',1)`, prID, orgID)
	require.NoError(t, err, "seed PR")
	return pool, orgID, repoID, prID
}

func testRepairRetainedFullController(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID, status, publicationState string) {
	ctx := context.Background()
	assessmentID, sessionID, foreignOrg := uuid.New(), uuid.New(), uuid.New()
	outputKey := "full-controller:" + assessmentID.String()
	dedupe := "code_review:" + outputKey
	payload := json.RawMessage(`{"org_id":"` + org.String() + `","session_id":"` + sessionID.String() + `","review_output_key":"` + outputKey + `","request_id":"retained-request","fork_context":{"from_fork":true},"dispute_context":{"reason":"specific concern"}}`)
	foreignPayload := json.RawMessage(`{"org_id":"` + foreignOrg.String() + `","session_id":"` + sessionID.String() + `","review_output_key":"` + outputKey + `","request_id":"foreign-request"}`)
	_, err := pool.Exec(ctx, `INSERT INTO organizations(id) VALUES($1)`, foreignOrg)
	require.NoError(t, err, "seed foreign tenant")
	_, err = pool.Exec(ctx, `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,pull_request_id,session_id,review_scope,status,result_origin,publication_state,publication_key) VALUES($1,$2,$3,$4,$5,'full',$6,'executed',$7,$8)`, assessmentID, org, repo, pr, sessionID, status, publicationState, outputKey)
	require.NoError(t, err, "seed staged or confirmed full assessment")
	_, err = pool.Exec(ctx, `INSERT INTO jobs(org_id,queue,job_type,payload,dedupe_key,status,created_at) VALUES($1,'agent','run_code_review',$2,$3,'dead_letter',now()-interval '1 minute'),($4,'agent','run_code_review',$5,$3,'succeeded',now())`, org, payload, dedupe, foreignOrg, foreignPayload)
	require.NoError(t, err, "retain exact terminal controller and conflicting foreign payload")
	store := NewCodeReviewScheduleStore(pool)
	require.NoError(t, store.RepairMissingWakes(ctx), "repair terminal full controller")
	require.NoError(t, store.RepairMissingWakes(ctx), "repeat repair without duplicate controller")
	var restored json.RawMessage
	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT payload FROM jobs WHERE org_id=$1 AND job_type='run_code_review' AND dedupe_key=$2 AND status='pending'`, org, dedupe).Scan(&restored), "read recovered controller payload")
	require.JSONEq(t, string(payload), string(restored), "repair must retain request, fork, dispute and publication provenance byte-for-byte as JSON")
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM jobs WHERE org_id=$1 AND dedupe_key=$2 AND status='pending'`, org, dedupe).Scan(&count), "count tenant controller wakes")
	require.Equal(t, 1, count, "one retained full controller should be active")
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM jobs WHERE org_id=$1 AND status='pending'`, foreignOrg).Scan(&count), "count foreign tenant wakes")
	require.Equal(t, 0, count, "foreign terminal job must not become the tenant replacement")
}

func testRepairExpiredUncertainPublication(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID) {
	ctx := context.Background()
	assessmentID, sessionID := uuid.New(), uuid.New()
	outputKey := "uncertain:" + assessmentID.String()
	_, err := pool.Exec(ctx, `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,pull_request_id,session_id,review_scope,status,result_origin,publication_state,publication_key,created_at) VALUES($1,$2,$3,$4,$5,'full','publishing','executed','uncertain',$6,now()-interval '3 hours')`, assessmentID, org, repo, pr, sessionID, outputKey)
	require.NoError(t, err, "seed expired uncertain publication")
	_, err = pool.Exec(ctx, `INSERT INTO jobs(org_id,queue,job_type,payload,dedupe_key,status) VALUES($1,'agent','run_code_review',$2,$3,'dead_letter')`, org, json.RawMessage(`{"session_id":"`+sessionID.String()+`","review_output_key":"`+outputKey+`"}`), "code_review:"+outputKey)
	require.NoError(t, err, "retain original terminal publication job")
	store := NewCodeReviewScheduleStore(pool)
	require.NoError(t, store.WithLockedPR(ctx, org, repo, pr, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
		state.ActiveAssessmentID = &assessmentID
		state.State = models.CodeReviewScheduleRunning
		return nil
	}), "persist publication fence in scheduler")
	require.NoError(t, store.RepairMissingWakes(ctx), "mark expired publication for operator reconciliation")
	require.NoError(t, store.RepairMissingWakes(ctx), "repeat expiration repair idempotently")
	var detail, status, publicationState string
	require.NoError(t, pool.QueryRow(ctx, `SELECT failure_detail,status,publication_state FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, org, assessmentID).Scan(&detail, &status, &publicationState), "read unresolved assessment")
	require.Equal(t, CodeReviewPublicationOperatorRequired, detail, "expired uncertain send requires operator reconciliation")
	require.Equal(t, "publishing", status, "operator handoff must retain active assessment status")
	require.Equal(t, "uncertain", publicationState, "operator handoff must retain uncertain send fence")
	state, err := store.Get(ctx, org, pr)
	require.NoError(t, err, "read fenced scheduler")
	require.Equal(t, &assessmentID, state.ActiveAssessmentID, "operator handoff must not release PR admission")
	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM jobs WHERE org_id=$1 AND status='pending' AND job_type='run_code_review'`, org).Scan(&count), "count renewed full supervisors")
	require.Equal(t, 0, count, "expired uncertain publication must not requeue automatic supervisor")
}

func testRepairSupersededEvidenceRefresh(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID) {
	ctx := context.Background()
	assessmentID := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,pull_request_id,review_scope,status,failure_detail) VALUES($1,$2,$3,$4,'evidence_only','superseded','evidence_recheck:inputs changed')`, assessmentID, org, repo, pr)
	require.NoError(t, err, "seed interrupted evidence refresh")
	store := NewCodeReviewScheduleStore(pool)
	require.NoError(t, store.RepairMissingWakes(ctx), "restore evidence refresh wake")
	require.NoError(t, store.RepairMissingWakes(ctx), "repeat refresh repair idempotently")
	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM jobs WHERE org_id=$1 AND job_type='run_code_review_recheck' AND dedupe_key=$2 AND status='pending'`, org, "code_review_recheck:"+assessmentID.String()).Scan(&count), "count restored refresh supervisors")
	require.Equal(t, 1, count, "one supervisor retries the interrupted evidence refresh")
}

func TestCodeReviewSchedulingPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(*testing.T, *pgxpool.Pool, uuid.UUID, uuid.UUID, uuid.UUID)
	}{
		{"repair restores retained staged full controller", func(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID) {
			testRepairRetainedFullController(t, pool, org, repo, pr, "running", "reserved")
		}},
		{"repair restores confirmed full controller", func(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID) {
			testRepairRetainedFullController(t, pool, org, repo, pr, "publishing", "confirmed")
		}},
		{"expired uncertain publication retains operator fence", testRepairExpiredUncertainPublication},
		{"superseded evidence refresh gets one supervisor", testRepairSupersededEvidenceRefresh},
		{"active evidence assessment blocks and repair restores supervisor", func(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID) {
			ctx := context.Background()
			assessmentID := uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,pull_request_id,review_scope,status,head_sha,base_sha,base_ref) VALUES($1,$2,$3,$4,'evidence_only','publishing','head','base','main')`, assessmentID, org, repo, pr)
			require.NoError(t, err, "seed uncertain assessment")
			store := NewCodeReviewScheduleStore(pool)
			require.NoError(t, store.WithLockedPR(ctx, org, repo, pr, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
				active, err := HasActiveCodeReview(ctx, tx, org, pr, time.Minute)
				require.NoError(t, err, "check active assessment")
				require.True(t, active, "publishing evidence assessment blocks new review")
				return nil
			}), "check serialized admission")
			require.NoError(t, store.RepairMissingWakes(ctx), "repair missing supervisor")
			require.NoError(t, store.RepairMissingWakes(ctx), "repair remains idempotent")
			var count int
			require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM jobs WHERE org_id=$1 AND dedupe_key=$2 AND status='pending'`, org, "code_review_recheck:"+assessmentID.String()).Scan(&count), "count supervisor jobs")
			require.Equal(t, 1, count, "one supervisor resumes uncertain publication")
			_, err = pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status='completed' WHERE org_id=$1 AND id=$2`, org, assessmentID)
			require.NoError(t, err, "complete assessment")
			require.NoError(t, store.WithLockedPR(ctx, org, repo, pr, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
				state.HeadSHA, state.BaseSHA, state.BaseRef = "head", "base", "main"
				state.ActiveAssessmentID, state.CurrentAssessmentID = &assessmentID, &assessmentID
				state.State = models.CodeReviewScheduleRunning
				return nil
			}), "record running assessment in scheduler")
			require.NoError(t, store.SettleAssessment(ctx, org, assessmentID), "settle completed assessment")
			settled, err := store.Get(ctx, org, pr)
			require.NoError(t, err, "read settled scheduler")
			require.Nil(t, settled.ActiveAssessmentID, "terminal assessment releases active pointer")
			require.Equal(t, &assessmentID, settled.CurrentAssessmentID, "completed assessment remains current")
			require.Equal(t, models.CodeReviewScheduleCovered, settled.State, "matching completed head is covered")
			require.NoError(t, store.SettleAssessment(ctx, org, assessmentID), "terminal settlement is idempotent")
			failedID := uuid.New()
			_, err = pool.Exec(ctx, `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,pull_request_id,review_scope,status,generation) VALUES($1,$2,$3,$4,'evidence_only','failed',2)`, failedID, org, repo, pr)
			require.NoError(t, err, "seed failed successor")
			require.NoError(t, store.WithLockedPR(ctx, org, repo, pr, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
				state.ActiveAssessmentID, state.CurrentAssessmentID = &failedID, &failedID
				state.State = models.CodeReviewScheduleRunning
				return nil
			}), "record failed assessment pointer")
			_, err = pool.Exec(ctx, `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,pull_request_id,review_scope,status,generation,superseded_by_assessment_id) VALUES($1,$2,$3,$4,'evidence_only','completed',3,$5)`, uuid.New(), org, repo, pr, failedID)
			require.NoError(t, err, "seed newer completed result that was explicitly superseded")
			require.NoError(t, store.SettleAssessment(ctx, org, failedID), "settle failed successor")
			settled, err = store.Get(ctx, org, pr)
			require.NoError(t, err, "read restored scheduler")
			require.Equal(t, &assessmentID, settled.CurrentAssessmentID, "failed recheck restores completed result")
			require.Nil(t, settled.ActiveAssessmentID, "failed recheck releases active pointer")
			require.Equal(t, models.CodeReviewScheduleIdle, settled.State, "failure does not declare latest head covered")
			require.NoError(t, store.WithLockedPR(ctx, org, repo, pr, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
				active, err := HasActiveCodeReview(ctx, tx, org, pr, time.Minute)
				require.NoError(t, err, "check completed assessment")
				require.False(t, active, "terminal assessment releases review admission")
				return nil
			}), "check terminal release")
		}},
		{"repair recovers stranded full assessment without pending input", func(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID) {
			ctx := context.Background()
			store := NewCodeReviewScheduleStore(pool)
			assessment, metadata, session := uuid.New(), uuid.New(), uuid.New()
			require.NoError(t, store.WithLockedPR(ctx, org, repo, pr, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
				state.State = models.CodeReviewScheduleRunning
				state.ActiveAssessmentID = &assessment
				return nil
			}), "retain stranded assessment pointer without queued replacement")
			_, err := pool.Exec(ctx, `INSERT INTO code_review_session_metadata(id,org_id,session_id,pull_request_id,status) VALUES($1,$2,$3,$4,'stale')`, metadata, org, session, pr)
			require.NoError(t, err, "seed terminal review metadata")
			_, err = pool.Exec(ctx, `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,pull_request_id,session_id,metadata_id,review_scope,status) VALUES($1,$2,$3,$4,$5,$6,'full','running')`, assessment, org, repo, pr, session, metadata)
			require.NoError(t, err, "seed orphaned full assessment")
			require.NoError(t, store.RepairMissingWakes(ctx), "restore recovery wake without pending input or live threads")
			require.NoError(t, store.RepairMissingWakes(ctx), "repeated sweep should preserve one wake")
			var count int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type=$2 AND status='pending'`, org, models.JobTypeReconcileCodeReviewSchedule).Scan(&count), "read durable repair wake")
			require.Equal(t, 1, count, "stranded full assessment should have exactly one recovery wake")
			require.NoError(t, store.ReconcileTerminalReviews(ctx, org, repo, pr), "recover after original controller disappeared")
			state, err := store.Get(ctx, org, pr)
			require.NoError(t, err, "read repaired schedule")
			require.Equal(t, models.CodeReviewScheduleIdle, state.State, "recovery must not leave schedule running after retiring its only assessment")
			require.Nil(t, state.ActiveAssessmentID, "terminal assessment must release active pointer")
		}},
		{"repair recovers closed PR cancellation and pending requests", func(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID) {
			ctx := context.Background()
			store := NewCodeReviewScheduleStore(pool)
			require.NoError(t, store.WithLockedPR(ctx, org, repo, pr, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
				state.State = models.CodeReviewScheduleClosed
				return nil
			}), "save closed state before cancellation")
			session := uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO code_review_session_metadata(org_id,session_id,pull_request_id,status) VALUES($1,$2,$3,'stale')`, org, session, pr)
			require.NoError(t, err, "save stale metadata")
			_, err = pool.Exec(ctx, `INSERT INTO session_threads VALUES($1,$2,'running')`, org, session)
			require.NoError(t, err, "simulate interruption before thread cancellation")
			require.NoError(t, store.RepairMissingWakes(ctx), "recover cancellation wake even with no pending input")
			require.NoError(t, store.RepairMissingWakes(ctx), "repeat repair without duplicate wake")
			ids, err := store.StaleActiveSessions(ctx, org, pr)
			require.NoError(t, err, "reload cancellation work")
			require.Equal(t, []uuid.UUID{session}, ids, "stale live thread must be retried after restart")
			foreign, err := store.StaleActiveSessions(ctx, uuid.New(), pr)
			require.NoError(t, err, "query foreign tenant")
			require.Empty(t, foreign, "foreign org must not see cancellation targets")
			var count int
			require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM jobs WHERE org_id=$1`, org).Scan(&count), "count wakes")
			require.Equal(t, 1, count, "closed PR has exactly one cancellation wake")
		}},
		{"stranded metadata does not block but draining threads do", func(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID) {
			ctx := context.Background()
			session := uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO code_review_session_metadata(org_id,session_id,pull_request_id,status,created_at,review_output_key) VALUES($1,$2,$3,'queued',now()-interval '1 hour','lost-job')`, org, session, pr)
			require.NoError(t, err, "seed stranded queued attempt")
			store := NewCodeReviewScheduleStore(pool)
			require.NoError(t, store.WithLockedPR(ctx, org, repo, pr, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
				active, err := HasActiveCodeReview(ctx, tx, org, pr, time.Minute)
				require.NoError(t, err, "check stranded attempt")
				require.False(t, active, "no job or thread remains after grace period")
				_, err = tx.Exec(ctx, `INSERT INTO session_threads VALUES($1,$2,'running')`, org, session)
				require.NoError(t, err, "simulate thread still draining")
				active, err = HasActiveCodeReview(ctx, tx, org, pr, time.Minute)
				require.NoError(t, err, "check live thread")
				require.True(t, active, "a live thread blocks even if its starter job is lost")
				return nil
			}), "serialize eligibility check")
		}},
		{"concurrent generations and duplicate request", func(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID) {
			store := NewCodeReviewScheduleStore(pool)
			ctx := context.Background()
			identity := uuid.NewString()
			var wg sync.WaitGroup
			errs := make(chan error, 10)
			for range 10 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs <- store.WithLockedPR(ctx, org, repo, pr, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
						_, duplicate, err := RecordCodeReviewRequest(ctx, tx, org, repo, pr, "ui", identity, models.CodeReviewReviewNow, "same-input", state.Generation+1, nil)
						if err != nil {
							return err
						}
						if !duplicate {
							state.Generation++
						}
						return nil
					})
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				require.NoError(t, err, "concurrent redeliveries serialize")
			}
			state, err := store.Get(ctx, org, pr)
			require.NoError(t, err, "read committed state")
			require.Equal(t, int64(1), state.Generation, "duplicate intent advances the generation only once")
			err = store.WithLockedPR(ctx, org, repo, pr, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
				_, _, err := RecordCodeReviewRequest(ctx, tx, org, repo, pr, "ui", identity, models.CodeReviewReviewNow, "different-input", 2, nil)
				return err
			})
			require.ErrorIs(t, err, ErrCodeReviewRequestConflict, "identity with different input conflicts")
		}},
		{"earlier later running and lost lease wakes", func(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID) {
			store := NewCodeReviewScheduleStore(pool)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Microsecond)
			for _, offset := range []time.Duration{5 * time.Minute, 15 * time.Minute, time.Minute} {
				at := now.Add(offset)
				err := store.WithLockedPR(ctx, org, repo, pr, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
					state.EligibleAt = &at
					return UpsertCodeReviewWake(ctx, tx, org, pr, at)
				})
				require.NoError(t, err, "reschedule pending wake")
				var actual time.Time
				require.NoError(t, pool.QueryRow(ctx, `SELECT run_at FROM jobs WHERE org_id=$1`, org).Scan(&actual), "read single wake deadline")
				require.Equal(t, at, actual.UTC(), "existing pending job moves earlier and later")
			}
			token := uuid.New()
			var jobID uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `UPDATE jobs SET status='running',lock_token=$2,attempts=1 WHERE org_id=$1 RETURNING id`, org, token).Scan(&jobID), "claim wake")
			err := store.WithLockedPR(ctx, org, repo, pr, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
				at := now.Add(20 * time.Minute)
				state.EligibleAt = &at
				return UpsertCodeReviewWake(ctx, tx, org, pr, at)
			})
			require.NoError(t, err, "running wake leaves current lease intact")
			err = store.WithLockedPR(ctx, org, repo, pr, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
				ok, err := NewJobStore(tx).RetryWithoutConsumingAttemptWithLease(ctx, jobID, token, "waiting", *state.EligibleAt)
				require.True(t, ok, "owning worker requeues the existing job")
				return err
			})
			require.NoError(t, err, "wake consumes latest deadline under PR lock")
			ok, err := NewJobStore(pool).MarkSucceededWithLease(ctx, jobID, token)
			require.NoError(t, err, "stale completion is safely ignored")
			require.False(t, ok, "outer worker cannot complete a requeued job with its former lease")
			var count, attempts int
			var at time.Time
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*),min(attempts),min(run_at) FROM jobs WHERE org_id=$1 AND status='pending'`, org).Scan(&count, &attempts, &at), "read durable wake")
			require.Equal(t, 1, count, "exactly one pending wake survives")
			require.Equal(t, 0, attempts, "waiting consumes no failure attempts")
			require.Equal(t, now.Add(20*time.Minute), at.UTC(), "latest target deadline survives stale completion")
		}},
		{"tenant mismatch rolls back state", func(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID) {
			store := NewCodeReviewScheduleStore(pool)
			err := store.WithLockedPR(context.Background(), uuid.New(), repo, pr, func(pgx.Tx, *models.CodeReviewPRState) error { return nil })
			require.ErrorIs(t, err, pgx.ErrNoRows, "cross-org PR cannot be scheduled")
			_, err = store.Get(context.Background(), org, pr)
			require.ErrorIs(t, err, pgx.ErrNoRows, "failed ownership check leaves no state")
		}},
		{"migration down and up", func(t *testing.T, pool *pgxpool.Pool, org, repo, pr uuid.UUID) {
			for _, direction := range []string{"down", "up"} {
				sql, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000289_code_review_scheduling."+direction+".sql"))
				require.NoError(t, err, "read migration direction")
				_, err = pool.Exec(context.Background(), string(sql))
				require.NoError(t, err, "migration can roll back and reapply on disposable data")
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, org, repo, pr := newSchedulingPostgres(t)
			tt.run(t, pool, org, repo, pr)
		})
	}
}
