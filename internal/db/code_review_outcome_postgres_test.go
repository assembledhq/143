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
	"github.com/stretchr/testify/require"
)

func TestCodeReviewFreshnessRetryPhaseCheckpointPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for durable freshness phase fencing proof")
	}
	tests := []struct {
		name, fence string
		owned       bool
	}{
		{name: "first phase and lease restart preserve deadline", owned: true},
		{name: "wrong organization", fence: "org"},
		{name: "wrong session", fence: "session"},
		{name: "wrong job", fence: "job"},
		{name: "wrong lease", fence: "lease"},
		{name: "terminal controller", fence: "status"},
		{name: "different job type", fence: "type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			conn, err := pgx.Connect(ctx, os.Getenv("TEST_DATABASE_URL"))
			require.NoError(t, err, "connect disposable freshness checkpoint database")
			defer func() { require.NoError(t, conn.Close(ctx), "close phase checkpoint connection") }()
			tx, err := conn.Begin(ctx)
			require.NoError(t, err, "isolate phase checkpoint fixtures")
			defer func() { require.NoError(t, tx.Rollback(ctx), "discard phase checkpoint fixtures") }()
			_, err = tx.Exec(ctx, `CREATE TEMP TABLE jobs(id uuid,org_id uuid,lock_token uuid,status text,job_type text,payload jsonb,updated_at timestamptz,retry_window_started_at timestamptz) ON COMMIT DROP`)
			require.NoError(t, err, "create the existing durable job fields")
			org, session, job, lease := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			oldStart := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
			_, err = tx.Exec(ctx, `INSERT INTO jobs VALUES($1,$2,$3,'running','run_code_review',jsonb_build_object('session_id',$4::text),now(),$5)`, job, org, lease, session, oldStart)
			require.NoError(t, err, "retain an older unrelated external retry deadline")
			callOrg, callSession, callJob, callLease := org, session, job, lease
			switch tt.fence {
			case "org":
				callOrg = uuid.New()
			case "session":
				callSession = uuid.New()
			case "job":
				callJob = uuid.New()
			case "lease":
				callLease = uuid.New()
			case "status":
				_, err = tx.Exec(ctx, `UPDATE jobs SET status='succeeded'`)
			case "type":
				_, err = tx.Exec(ctx, `UPDATE jobs SET job_type='run_agent'`)
			}
			require.NoError(t, err, "apply the requested ownership fence")
			started, owned, err := NewCodeReviewStore(tx).EnsureFreshnessRetryStartedAt(ctx, callOrg, callSession, callJob, callLease)
			require.NoError(t, err, "phase checkpoint fencing must complete without a database error")
			require.Equal(t, tt.owned, owned, "only the exact active controller may start its verification phase")
			var prior time.Time
			var checkpoint *time.Time
			require.NoError(t, tx.QueryRow(ctx, `SELECT retry_window_started_at,(payload->>'_code_review_freshness_retry_started_at')::timestamptz FROM jobs`).Scan(&prior, &checkpoint), "read both independent durable phase deadlines")
			require.Equal(t, oldStart, prior.UTC(), "starting freshness must leave the older external retry deadline untouched")
			if !tt.owned {
				require.Nil(t, checkpoint, "an unowned controller cannot create freshness state")
				return
			}
			require.WithinDuration(t, time.Now(), started, 5*time.Second, "freshness must receive its own first-attempt deadline")
			require.Equal(t, &started, checkpoint, "the phase start must be stored before the controller retries")
			newLease := uuid.New()
			_, err = tx.Exec(ctx, `UPDATE jobs SET lock_token=$1`, newLease)
			require.NoError(t, err, "simulate a worker restart reclaiming the same controller")
			again, owned, err := NewCodeReviewStore(tx).EnsureFreshnessRetryStartedAt(ctx, org, session, job, newLease)
			require.NoError(t, err, "the reclaimed controller must reuse its durable phase checkpoint")
			require.True(t, owned, "the reclaimed fencing token must own the same controller")
			require.Equal(t, started, again, "ordinary retries and restarts cannot extend the freshness deadline")
		})
	}
}

func TestCodeReviewUnpublishedOutcomeProjectionPostgres(t *testing.T) {
	t.Parallel()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL for effective outcome projection proof")
	}
	tests := []struct {
		name, status                       string
		posted, assessment, expectDecision bool
		rank                               int
	}{
		{name: "legacy cancelled blocked", status: "cancelled", rank: 6},
		{name: "legacy failed blocked", status: "failed", rank: 6},
		{name: "completed outcome retained", status: "completed", expectDecision: true, rank: 3},
		{name: "posted cancelled history retained", status: "cancelled", posted: true, expectDecision: true, rank: 3},
		{name: "historical completed assessment retained", status: "failed", assessment: true, expectDecision: true, rank: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			conn, err := pgx.Connect(ctx, dsn)
			require.NoError(t, err, "connect disposable projection database")
			defer func() { require.NoError(t, conn.Close(ctx), "close projection connection") }()
			tx, err := conn.Begin(ctx)
			require.NoError(t, err, "isolate projection scenario transaction")
			defer func() { require.NoError(t, tx.Rollback(ctx), "discard projection fixtures") }()
			schema := "review_outcome_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			_, err = tx.Exec(ctx, `CREATE SCHEMA `+schema+`; SET LOCAL search_path TO `+schema+`, public`)
			require.NoError(t, err, "isolate parallel projection scenario")
			_, err = tx.Exec(ctx, `CREATE TABLE code_review_session_metadata(id uuid,org_id uuid,session_id uuid,status text,stale bool,superseded_by_session_id uuid,decision text,acceptable bool,github_review_id bigint); CREATE TABLE code_review_revision_assessments(org_id uuid,session_id uuid,status text,superseded_by_assessment_id uuid,generation bigint,decision text,acceptable bool,risk_reason_details jsonb,rendered_body text,github_review_id bigint,github_review_url text,completed_at timestamptz,publication_key text)`)
			require.NoError(t, err, "create exact outcome expression inputs")
			id, org, session := uuid.New(), uuid.New(), uuid.New()
			_, err = tx.Exec(ctx, `INSERT INTO code_review_session_metadata VALUES($1,$2,$3,$4,false,NULL,'blocked',false,CASE WHEN $5 THEN 77 ELSE NULL END)`, id, org, session, tt.status, tt.posted)
			require.NoError(t, err, "seed historical blocked metadata")
			if tt.assessment {
				_, err = tx.Exec(ctx, `INSERT INTO code_review_revision_assessments(org_id,session_id,status,generation,decision,acceptable) VALUES($1,$2,'completed',1,'blocked',false)`, org, session)
				require.NoError(t, err, "retain a genuine completed assessment for the same session")
			}
			var decision *models.CodeReviewDecision
			var acceptable *bool
			var sqlRank int
			err = tx.QueryRow(ctx, `SELECT `+codeReviewCurrentDecision+`,`+codeReviewCurrentAcceptable+`,`+codeReviewOutcomeRankSQL+` FROM code_review_session_metadata m`+codeReviewCurrentAssessmentJoin+` WHERE m.org_id=$1 AND m.id=$2`, org, id).Scan(&decision, &acceptable, &sqlRank)
			require.NoError(t, err, "effective outcome projection and ordering must execute")
			if tt.expectDecision {
				require.Equal(t, new(models.CodeReviewDecisionBlocked), decision, "genuine historical outcomes must remain visible")
				require.Equal(t, new(false), acceptable, "genuine historical risk acceptance must remain visible")
			} else {
				require.Nil(t, decision, "unpublished terminal worker failures must have no outcome")
				require.Nil(t, acceptable, "unpublished terminal worker failures must have no risk acceptance")
			}
			require.Equal(t, tt.rank, sqlRank, "effective outcome must sort by the displayed label")
			item := models.CodeReviewListItem{CompletedAssessmentOutcome: tt.assessment, CodeReviewSessionMetadata: models.CodeReviewSessionMetadata{Status: models.CodeReviewSessionStatus(tt.status), Decision: new(models.CodeReviewDecisionBlocked)}}
			if tt.posted {
				item.GitHubReviewID = new(int64(77))
			}
			// The PR's current summary can belong to a newer session. Cursor
			// provenance must come from the exact session projected by SQL.
			item.CurrentAssessment = &models.CodeReviewAssessmentSummary{SessionID: uuid.New(), Status: models.CodeReviewAssessmentCompleted}
			rank, ok := CodeReviewSortRankForItem("outcome", item)
			require.True(t, ok, "the effective outcome must have a cursor rank")
			require.Equal(t, sqlRank, rank, "historical session projection must rank correctly even after a different PR session completes")
			blocked := models.CodeReviewDecisionBlocked
			where, args, err := codeReviewListWhere(org, CodeReviewListFilters{Decision: &blocked}, false)
			require.NoError(t, err, "build the blocked filter using effective outcome semantics")
			var matched int
			require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM code_review_session_metadata m`+codeReviewCurrentAssessmentJoin+where, args).Scan(&matched), "the actual blocked filter must execute")
			want := 0
			if tt.expectDecision {
				want = 1
			}
			require.Equal(t, want, matched, "the blocked filter must include only rows that display a blocked outcome")
		})
	}
}
