package db

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestJobStoreCodeReviewPreparationCheckpoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		found bool
	}{{name: "first preparation remains visible across generations", found: true}, {name: "another tenant has no checkpoint"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "checkpoint database mock should initialize")
			defer mock.Close()
			orgID, reviewID, sessionID := uuid.New(), uuid.New(), uuid.New()
			first := time.Now().UTC().Add(-7 * time.Minute)
			rows := pgxmock.NewRows([]string{"created_at"})
			if tt.found {
				rows.AddRow(first)
			}
			mock.ExpectQuery(`(?s)SELECT created_at FROM jobs.*org_id = \$1.*job_type = 'prepare_code_review_workspace'.*payload->>'metadata_id' = \$2.*payload->>'session_id' = \$3.*ORDER BY created_at ASC`).WithArgs(orgID, reviewID.String(), sessionID.String()).WillReturnRows(rows)
			actual, err := NewJobStore(mock).FirstCodeReviewWorkspacePreparationCreatedAt(context.Background(), orgID, reviewID, sessionID)
			if tt.found {
				require.NoError(t, err, "reviewer retries should retain the first durable preparation enqueue")
				require.Equal(t, first, actual, "generation changes must not renew the initial preparation budget")
			} else {
				require.ErrorIs(t, err, pgx.ErrNoRows, "a different tenant must not inherit another review's preparation")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "checkpoint lookup must include tenant, review and session identity")
		})
	}
}

func TestCodeReviewPreparationOutcomeIsExactGeneration(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "outcome database mock should initialize")
	defer mock.Close()
	orgID := uuid.New()
	key := "code_review_prepare:review:session:2"
	detail := "Review workspace preparation failed during repository clone. Review did not run."
	mock.ExpectQuery(`(?s)SELECT status, LEFT\(COALESCE\(last_error, ''\), 512\) FROM jobs.*org_id = \$1 AND queue = \$2 AND dedupe_key = \$3.*job_type = 'prepare_code_review_workspace'`).WithArgs(orgID, "agent", key).WillReturnRows(pgxmock.NewRows([]string{"status", "last_error"}).AddRow(models.JobStatusDeadLetter, detail))
	status, actual, err := NewJobStore(mock).LatestCodeReviewWorkspacePreparationOutcome(context.Background(), orgID, "agent", key)
	require.NoError(t, err, "controller should read the exact completed preparation checkpoint")
	require.Equal(t, models.JobStatusDeadLetter, status, "failed setup must not be interpreted as successful readiness")
	require.Equal(t, detail, actual, "bounded diagnostics remain available for finite safe-message classification")
	require.NoError(t, mock.ExpectationsWereMet(), "outcome must not import another generation's failure")
}

func extendWorkspaceFailureFixture(t *testing.T, f reviewWorkspaceFixture) uuid.UUID {
	t.Helper()
	prID := uuid.New()
	_, err := f.pool.Exec(context.Background(), `ALTER TABLE code_review_session_metadata
 ADD COLUMN pull_request_id uuid, ADD COLUMN base_sha text NOT NULL DEFAULT 'base-1',
 ADD COLUMN from_fork boolean NOT NULL DEFAULT false, ADD COLUMN trigger_source text NOT NULL DEFAULT 'app_reviewer',
 ADD COLUMN phase text, ADD COLUMN status_code text, ADD COLUMN status_message text, ADD COLUMN retry_at timestamptz,
 ADD COLUMN last_error_at timestamptz, ADD COLUMN retryable_failure boolean NOT NULL DEFAULT false,
 ADD COLUMN decision text, ADD COLUMN acceptable boolean, ADD COLUMN superseded_by_session_id uuid,
 ADD COLUMN review_output_key text NOT NULL DEFAULT 'test-output', ADD COLUMN prompt_record_key text,
 ADD COLUMN github_review_id bigint, ADD COLUMN github_review_url text, ADD COLUMN final_review_body text,
 ADD COLUMN failure_reason text, ADD COLUMN completed_at timestamptz, ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();
 CREATE TABLE code_review_revision_assessments(org_id uuid NOT NULL,metadata_id uuid NOT NULL,session_id uuid NOT NULL,
 publication_state text NOT NULL,publication_receipt jsonb,github_review_id bigint,result_origin text);
 ALTER TABLE jobs ADD COLUMN last_error text;`)
	require.NoError(t, err, "extend the isolated fixture with operational projection and receipt fields")
	_, err = f.pool.Exec(context.Background(), `UPDATE code_review_session_metadata SET pull_request_id=$1 WHERE org_id=$2 AND id=$3`, prID, f.org, f.review)
	require.NoError(t, err, "bind operational failure to the exact pull request")
	return prID
}

func TestCodeReviewWorkspaceFailureFencePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, setup     string
		foreignOrg      bool
		expectedFailure bool
	}{
		{name: "current initial setup failure", expectedFailure: true},
		{name: "newer workspace generation", setup: "UPDATE sessions SET workspace_generation=1"},
		{name: "changed review head", setup: "UPDATE code_review_session_metadata SET head_sha='new-head'"},
		{name: "cancelled review", setup: "UPDATE code_review_session_metadata SET status='cancelled'"},
		{name: "other tenant", foreignOrg: true},
		{name: "uncertain publication", setup: "INSERT INTO code_review_revision_assessments SELECT org_id,id,session_id,'uncertain',NULL,NULL,NULL FROM code_review_session_metadata"},
		{name: "publication receipt", setup: "INSERT INTO code_review_revision_assessments SELECT org_id,id,session_id,'not_started','{}',NULL,NULL FROM code_review_session_metadata"},
		{name: "staged result", setup: "INSERT INTO code_review_revision_assessments SELECT org_id,id,session_id,'not_started',NULL,NULL,'full_review' FROM code_review_session_metadata"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewWorkspaceFixture(t)
			prID := extendWorkspaceFailureFixture(t, f)
			if tt.setup != "" {
				_, err := f.pool.Exec(context.Background(), tt.setup)
				require.NoError(t, err, "set up the competing durable state in the disposable schema")
			}
			orgID := f.org
			if tt.foreignOrg {
				orgID = uuid.New()
			}
			reason := "Review workspace preparation failed during declared dependency and bootstrap setup. Review did not run. This is an operational review failure."
			actual, err := NewCodeReviewStore(f.pool).FailCodeReviewWorkspacePreparation(context.Background(), orgID, FailCodeReviewWorkspacePreparationParams{ReviewID: f.review, SessionID: f.session, PullRequestID: prID, HeadSHA: "head-1", ExpectedGeneration: 0, Reason: reason})
			if !tt.expectedFailure {
				require.ErrorIs(t, err, pgx.ErrNoRows, "a competing generation, head, tenant or publication must keep its existing state")
				return
			}
			require.NoError(t, err, "current setup failure should be projected under the publication and generation fence")
			require.Equal(t, models.CodeReviewSessionStatusFailed, actual.Status, "workspace failures are terminal operational attempts")
			require.Equal(t, models.CodeReviewDecisionBlocked, *actual.Decision, "a failed setup cannot produce approval")
			require.Equal(t, false, *actual.Acceptable, "failed setup cannot claim complete code coverage")
			require.Equal(t, &reason, actual.StatusMessage, "safe stage details should reach the dashboard and rolling comment")
			require.True(t, actual.RetryableFailure, "a corrected setup may be retried as a new review")
		})
	}
}

func TestCodeReviewWorkspaceFailureVersusPublicationPostgres(t *testing.T) {
	t.Parallel()
	f := newReviewWorkspaceFixture(t)
	prID := extendWorkspaceFailureFixture(t, f)
	type failureOutcome struct {
		metadata models.CodeReviewSessionMetadata
		err      error
	}
	type publicationOutcome struct {
		published bool
		err       error
	}
	failed := make(chan failureOutcome, 1)
	published := make(chan publicationOutcome, 1)
	go func() {
		metadata, err := NewCodeReviewStore(f.pool).FailCodeReviewWorkspacePreparation(context.Background(), f.org, FailCodeReviewWorkspacePreparationParams{ReviewID: f.review, SessionID: f.session, PullRequestID: prID, HeadSHA: "head-1", ExpectedGeneration: 0, Reason: "Operational workspace failure"})
		failed <- failureOutcome{metadata, err}
	}()
	go func() {
		ok, err := NewCodeReviewWorkspaceStore(f.pool).PublishPrepared(context.Background(), f.org, f.params)
		published <- publicationOutcome{ok, err}
	}()
	failure, publication := <-failed, <-published
	require.NoError(t, publication.err, "workspace publication race should be decided by fencing")
	if publication.published {
		require.ErrorIs(t, failure.err, pgx.ErrNoRows, "new workspace publication must invalidate the old generation failure")
	} else {
		require.NoError(t, failure.err, "a terminal setup failure must prevent later prepared-workspace publication")
		require.Equal(t, models.CodeReviewSessionStatusFailed, failure.metadata.Status, "the controller must win exactly one terminal fence")
	}
	require.False(t, publication.published && failure.err == nil, "a prepared workspace and a terminal setup failure cannot both win")
}

func TestCodeReviewPreparationBudgetSurvivesGenerationChangePostgres(t *testing.T) {
	t.Parallel()
	f := newReviewWorkspaceFixture(t)
	first := time.Now().UTC().Add(-7 * time.Minute).Truncate(time.Microsecond)
	payload := map[string]any{"metadata_id": f.review.String(), "session_id": f.session.String()}
	_, err := f.pool.Exec(context.Background(), `UPDATE jobs SET payload=$1,created_at=$2,status='succeeded' WHERE id=$3`, payload, first, f.job)
	require.NoError(t, err, "persist the first completed initialization checkpoint")
	_, err = f.pool.Exec(context.Background(), `INSERT INTO jobs(org_id,queue,job_type,payload,created_at) VALUES($1,'agent','prepare_code_review_workspace',$2,now())`, f.org, payload)
	require.NoError(t, err, "record a later generation recovery job")
	actual, err := NewJobStore(f.pool).FirstCodeReviewWorkspacePreparationCreatedAt(context.Background(), f.org, f.review, f.session)
	require.NoError(t, err, "load the first preparation across publication generations")
	require.Equal(t, first, actual.UTC(), "generation recovery must not grant another eight-minute initialization window")
	_, err = NewJobStore(f.pool).FirstCodeReviewWorkspacePreparationCreatedAt(context.Background(), uuid.New(), f.review, f.session)
	require.True(t, errors.Is(err, pgx.ErrNoRows), "the same review identifiers in another tenant must reveal no checkpoint")
}

func TestCodeReviewPreparationLookupIndexPostgres(t *testing.T) {
	t.Parallel()
	f := newReviewWorkspaceFixture(t)
	ctx := context.Background()
	up, err := os.ReadFile("../../migrations/000312_code_review_initial_preparation_lookup.up.sql")
	require.NoError(t, err, "read the standalone concurrent lookup migration")
	_, err = f.pool.Exec(ctx, string(up))
	require.NoError(t, err, "concurrent migration must apply outside a transaction in the disposable schema")
	_, err = f.pool.Exec(ctx, `UPDATE jobs SET payload=$1 WHERE id=$2`, map[string]any{"metadata_id": f.review.String(), "session_id": f.session.String()}, f.job)
	require.NoError(t, err, "seed the exact preparation checkpoint")
	_, err = f.pool.Exec(ctx, `INSERT INTO jobs(org_id,queue,job_type,payload)
 SELECT $1,'agent','prepare_code_review_workspace',jsonb_build_object('metadata_id',gen_random_uuid()::text,'session_id',gen_random_uuid()::text)
 FROM generate_series(1,2000)`, f.org)
	require.NoError(t, err, "seed unrelated jobs so planning can compare selective index access")
	_, err = f.pool.Exec(ctx, "ANALYZE jobs")
	require.NoError(t, err, "refresh statistics for the disposable lookup planning proof")
	rows, err := f.pool.Query(ctx, `EXPLAIN (COSTS OFF) SELECT created_at FROM jobs
 WHERE org_id=$1 AND queue='agent' AND job_type='prepare_code_review_workspace'
 AND payload->>'metadata_id'=$2 AND payload->>'session_id'=$3
 ORDER BY created_at ASC,id ASC LIMIT 1`, f.org, f.review.String(), f.session.String())
	require.NoError(t, err, "inspect the exact earliest-preparation query plan without executing the query")
	plan, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err, "read the PostgreSQL lookup plan")
	require.Contains(t, strings.Join(plan, "\n"), "idx_jobs_code_review_initial_preparation", "the partial expression index must service the exact tenant and review checkpoint")
	down, err := os.ReadFile("../../migrations/000312_code_review_initial_preparation_lookup.down.sql")
	require.NoError(t, err, "read the standalone concurrent rollback migration")
	_, err = f.pool.Exec(ctx, string(down))
	require.NoError(t, err, "concurrent rollback must remove only the new lookup index")
	var absent bool
	err = f.pool.QueryRow(ctx, "SELECT to_regclass('idx_jobs_code_review_initial_preparation') IS NULL").Scan(&absent)
	require.NoError(t, err, "confirm the disposable index rollback")
	require.True(t, absent, "down migration must remove the preparation lookup index")
}
