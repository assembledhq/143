package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewStoreSetWaitingForGitHubPostgres(t *testing.T) {
	t.Parallel()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL for the GitHub wait transition and retry fence proof")
	}
	tests := []struct {
		name       string
		fence      string
		shouldWait bool
	}{
		{name: "matching pending retry persists waiting metadata", shouldWait: true},
		{name: "stale retry timestamp leaves metadata unchanged", fence: "timestamp"},
		{name: "stale retry error leaves metadata unchanged", fence: "error"},
		{name: "claimed job leaves metadata unchanged", fence: "status"},
		{name: "wrong organization leaves metadata unchanged", fence: "org"},
		{name: "foreign organization job leaves metadata unchanged", fence: "job_org"},
		{name: "different job type leaves metadata unchanged", fence: "job_type"},
		{name: "completed review leaves metadata unchanged", fence: "metadata_status"},
		{name: "wrong payload session leaves metadata unchanged", fence: "session"},
		{name: "malformed payload session leaves metadata unchanged", fence: "malformed_session"},
		{name: "wrong job leaves metadata unchanged", fence: "job"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			tx := newGitHubWaitPostgres(t, ctx, databaseURL)
			phase := models.CodeReviewPhasePublishing
			statusMessage := "Publishing the synthetic review."
			promptRecordKey := "synthetic-prompt-record"
			finalReviewBody := "Synthetic review content."
			initial := models.CodeReviewSessionMetadata{
				ID: uuid.New(), OrgID: uuid.New(), SessionID: uuid.New(), RepositoryID: uuid.New(),
				PullRequestID: uuid.New(), PolicyID: uuid.New(),
				BaseSHA: "synthetic-base", HeadSHA: "synthetic-head",
				TriggerSource: models.CodeReviewTriggerSourceAppReviewer,
				Status:        models.CodeReviewSessionStatusRunning, Phase: &phase, StatusMessage: &statusMessage,
				ReviewOutputKey: "synthetic-review-output", PromptRecordKey: &promptRecordKey,
				FinalReviewBody: &finalReviewBody,
				CreatedAt:       time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC),
			}
			_, err := tx.Exec(ctx, `INSERT INTO code_review_session_metadata (`+codeReviewMetadataColumns+`)
VALUES ($1,$2,$3,$4,$5,$6,'synthetic-base','synthetic-head',false,'app_reviewer',
        'running','publishing',NULL,$7,NULL,NULL,false,NULL,NULL,false,NULL,
        'synthetic-review-output','synthetic-prompt-record',NULL,NULL,'Synthetic review content.',
        NULL,NULL,$8)`, initial.ID, initial.OrgID, initial.SessionID, initial.RepositoryID,
				initial.PullRequestID, initial.PolicyID, statusMessage, initial.CreatedAt)
			require.NoError(t, err, "seed the complete synthetic review metadata before its retry transition")
			jobID := uuid.New()
			retryAt := initial.CreatedAt.Add(5 * time.Minute)
			jobError := "Synthetic GitHub rate limit"
			_, err = tx.Exec(ctx, `INSERT INTO jobs (id,org_id,job_type,payload,status,run_at,last_error)
VALUES ($1,$2,'run_code_review',jsonb_build_object('session_id',$3::text),'pending',$4,$5)`,
				jobID, initial.OrgID, initial.SessionID.String(), retryAt, jobError)
			require.NoError(t, err, "seed the exact pending controller retry")

			callOrg, callJob := initial.OrgID, jobID
			callRetryAt, callError := retryAt, jobError
			switch tt.fence {
			case "timestamp":
				callRetryAt = retryAt.Add(-time.Minute)
			case "error":
				callError = "Earlier synthetic GitHub rate limit"
			case "status":
				_, err = tx.Exec(ctx, `UPDATE jobs SET status='running' WHERE org_id=$1 AND id=$2`, initial.OrgID, jobID)
			case "org":
				callOrg = uuid.New()
			case "job_org":
				_, err = tx.Exec(ctx, `UPDATE jobs SET org_id=$3 WHERE org_id=$1 AND id=$2`, initial.OrgID, jobID, uuid.New())
			case "job_type":
				_, err = tx.Exec(ctx, `UPDATE jobs SET job_type='continue_session' WHERE org_id=$1 AND id=$2`, initial.OrgID, jobID)
			case "metadata_status":
				initial.Status = models.CodeReviewSessionStatusCompleted
				_, err = tx.Exec(ctx, `UPDATE code_review_session_metadata SET status=$3 WHERE org_id=$1 AND session_id=$2`, initial.OrgID, initial.SessionID, initial.Status)
			case "session":
				_, err = tx.Exec(ctx, `UPDATE jobs SET payload=jsonb_build_object('session_id',$3::text) WHERE org_id=$1 AND id=$2`, initial.OrgID, jobID, uuid.NewString())
			case "malformed_session":
				_, err = tx.Exec(ctx, `UPDATE jobs SET payload=jsonb_build_object('session_id','not-a-uuid'::text) WHERE org_id=$1 AND id=$2`, initial.OrgID, jobID)
			case "job":
				callJob = uuid.New()
			}
			require.NoError(t, err, "apply the selected job or metadata fence before the wait transition")
			var transitionTime time.Time
			require.NoError(t, tx.QueryRow(ctx, `SELECT now()`).Scan(&transitionTime), "read the transaction timestamp used by the wait transition")
			message := "GitHub is rate-limited. The review will resume automatically."
			actual, err := NewCodeReviewStore(tx).SetWaitingForGitHub(ctx, callOrg, initial.SessionID, callJob, callRetryAt, callError, "  "+message+"  ")
			expected := initial
			if tt.shouldWait {
				require.NoError(t, err, "a matching pending retry should persist its GitHub wait without a PostgreSQL type error")
				waitPhase := models.CodeReviewPhaseWaitingGitHub
				statusCode := models.CodeReviewStatusCodeGitHubRateLimited
				expected.Phase = &waitPhase
				expected.StatusCode = &statusCode
				expected.StatusMessage = &message
				expected.RetryAt = &retryAt
				expected.LastErrorAt = &transitionTime
				expected.RetryableFailure = true
				require.Equal(t, expected, actual, "the wait update should return all original metadata and the exact retry details")
			} else {
				require.ErrorIs(t, err, pgx.ErrNoRows, "an unmatched retry fence should reject the transition without another database error")
				require.Equal(t, models.CodeReviewSessionMetadata{}, actual, "a rejected transition should return no metadata")
			}
			persisted, err := NewCodeReviewStore(tx).GetBySessionID(ctx, initial.OrgID, initial.SessionID)
			require.NoError(t, err, "read back the review metadata after the fenced transition")
			require.Equal(t, expected, persisted, "only the exact pending retry may change the persisted review metadata")
		})
	}
}

func newGitHubWaitPostgres(t *testing.T, ctx context.Context, databaseURL string) pgx.Tx {
	t.Helper()
	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err, "connect to the disposable GitHub wait PostgreSQL database")
	t.Cleanup(func() { require.NoError(t, conn.Close(ctx), "close the isolated GitHub wait database connection") })
	conn.TypeMap().RegisterType(&pgtype.Type{
		Name: "timestamptz", OID: pgtype.TimestamptzOID,
		Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC},
	})
	tx, err := conn.Begin(ctx)
	require.NoError(t, err, "begin the isolated GitHub wait fixture transaction")
	t.Cleanup(func() {
		require.NoError(t, tx.Rollback(ctx), "roll back the GitHub wait schema and synthetic fixtures")
	})
	schema := "review_github_wait_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = tx.Exec(ctx, `CREATE SCHEMA `+schema+`; SET LOCAL search_path TO `+schema+`,public`)
	require.NoError(t, err, "create an isolated schema for each parallel GitHub wait case")
	_, err = tx.Exec(ctx, `
CREATE TABLE code_review_session_metadata (
    id uuid PRIMARY KEY, org_id uuid NOT NULL, session_id uuid NOT NULL,
    repository_id uuid NOT NULL, pull_request_id uuid NOT NULL, policy_id uuid NOT NULL,
    base_sha text NOT NULL, head_sha text NOT NULL, from_fork boolean NOT NULL,
    trigger_source text NOT NULL, status text NOT NULL, phase text, status_code text,
    status_message text, retry_at timestamptz, last_error_at timestamptz,
    retryable_failure boolean NOT NULL, decision text, acceptable boolean,
    stale boolean NOT NULL, superseded_by_session_id uuid, review_output_key text NOT NULL,
    prompt_record_key text, github_review_id bigint, github_review_url text, final_review_body text,
    failure_reason text, completed_at timestamptz, created_at timestamptz NOT NULL
);
CREATE TABLE jobs (
    id uuid PRIMARY KEY, org_id uuid NOT NULL, job_type text NOT NULL,
    payload jsonb NOT NULL, status text NOT NULL, run_at timestamptz NOT NULL, last_error text
);`)
	require.NoError(t, err, "create the minimal typed metadata and job shapes used by the wait transition")
	return tx
}
