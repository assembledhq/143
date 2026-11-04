package db

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func newPriorHistoryPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run prior review history PostgreSQL tests")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err, "parse isolated history test database")
	// Two fixtures can run in parallel; each owns at most two connections,
	// including schema administration. No separate admin connection is held.
	cfg.MaxConns = 2
	cfg.MinConns = 0
	schema := "prior_history_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err, "create bounded history fixture pool")
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		require.NoError(t, err, "remove isolated history fixture schema")
		pool.Close()
	})
	_, err = pool.Exec(context.Background(), "CREATE SCHEMA "+schema)
	require.NoError(t, err, "create isolated history fixture schema")
	_, err = pool.Exec(context.Background(), `
 CREATE TABLE sessions(id uuid PRIMARY KEY, org_id uuid NOT NULL, created_at timestamptz NOT NULL);
 CREATE TABLE code_review_session_metadata(session_id uuid PRIMARY KEY, org_id uuid NOT NULL, repository_id uuid NOT NULL, pull_request_id uuid NOT NULL, policy_id uuid NOT NULL, base_sha text NOT NULL DEFAULT 'base', head_sha text NOT NULL DEFAULT 'head', status text NOT NULL DEFAULT 'completed', decision text DEFAULT 'blocked', stale boolean NOT NULL DEFAULT false, superseded_by_session_id uuid, final_review_body text, created_at timestamptz NOT NULL, completed_at timestamptz);
 CREATE TABLE code_review_revision_assessments(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,repository_id uuid NOT NULL,pull_request_id uuid NOT NULL,policy_id uuid NOT NULL,source_assessment_id uuid,base_sha text,head_sha text,status text,decision text,completed_at timestamptz,rendered_body text,generation bigint);
 CREATE TABLE code_review_prompt_records(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),org_id uuid NOT NULL,session_id uuid NOT NULL,record_key text NOT NULL,role text NOT NULL,agent_provider text NOT NULL DEFAULT '',content text NOT NULL DEFAULT '',metadata jsonb NOT NULL DEFAULT '{}',created_at timestamptz NOT NULL DEFAULT now(),UNIQUE(org_id,record_key));
 CREATE TABLE session_threads(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,label text NOT NULL DEFAULT 'review',agent_type text NOT NULL DEFAULT 'codex',status text NOT NULL DEFAULT 'completed',created_at timestamptz NOT NULL,archived_at timestamptz);
 CREATE TABLE session_messages(id bigint PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,thread_id uuid NOT NULL,role text NOT NULL DEFAULT 'assistant',turn_number integer NOT NULL DEFAULT 1,content text NOT NULL,created_at timestamptz NOT NULL);
 CREATE TABLE code_review_findings(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,assessment_id uuid,severity text NOT NULL DEFAULT 'high',confidence text NOT NULL DEFAULT 'high',path text,start_line integer,end_line integer,summary text NOT NULL,body text NOT NULL,github_comment_id bigint,created_at timestamptz NOT NULL);
 CREATE TABLE code_review_agent_results(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL,assessment_id uuid,agent_provider text NOT NULL DEFAULT 'codex',role text NOT NULL DEFAULT 'reviewer',status text NOT NULL DEFAULT 'completed',raw_output text,structured_result jsonb,created_at timestamptz NOT NULL);
 `)
	require.NoError(t, err, "create focused real SQL schema")
	return pool
}

func TestCodeReviewHistoryPostgresScopeAndAssessments(t *testing.T) {
	t.Parallel()
	pool := newPriorHistoryPostgres(t)
	ctx := context.Background()
	store := NewCodeReviewHistoryStore(pool)
	orgID, repoID, prID, policyID, currentID, priorID, olderID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	orchestratorID, reviewerID := uuid.New(), uuid.New()
	cutoff := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	before := cutoff.Add(-time.Hour)
	after := cutoff.Add(time.Hour)
	_, err := pool.Exec(ctx, `INSERT INTO sessions(id,org_id,created_at)VALUES($1,$2,$3);`, currentID, orgID, cutoff)
	require.NoError(t, err, "seed current review session")
	_, err = pool.Exec(ctx, `INSERT INTO code_review_session_metadata(session_id,org_id,repository_id,pull_request_id,policy_id,status,created_at)VALUES($1,$2,$3,$4,$5,'running',$6)`, currentID, orgID, repoID, prID, policyID, cutoff)
	require.NoError(t, err, "seed active review source")
	_, err = pool.Exec(ctx, `INSERT INTO session_threads(id,org_id,session_id,created_at)VALUES($1,$3,$4,$5),($2,$3,$4,$5)`, orchestratorID, reviewerID, orgID, currentID, cutoff)
	require.NoError(t, err, "seed independent reviewer and orchestrator threads")
	_, err = pool.Exec(ctx, `INSERT INTO code_review_prompt_records(org_id,session_id,record_key,role,metadata)VALUES($1,$2,'synthesis','orchestrator',jsonb_build_object('review_history_thread_id',$3::text))`, orgID, currentID, orchestratorID.String())
	require.NoError(t, err, "persist trusted pre-dispatch binding")
	cases := []struct {
		name              string
		id, org, repo, pr uuid.UUID
		created           time.Time
		completed         *time.Time
		allowed           bool
	}{
		{"prior round", priorID, orgID, repoID, prID, before, &before, true},
		{"older round", olderID, orgID, repoID, prID, before.Add(-time.Hour), &before, true},
		{"other org", uuid.New(), uuid.New(), repoID, prID, before, &before, false},
		{"other repository", uuid.New(), orgID, uuid.New(), prID, before, &before, false},
		{"other pull request", uuid.New(), orgID, repoID, uuid.New(), before, &before, false},
		{"same-time round", uuid.New(), orgID, repoID, prID, cutoff, &before, false},
		{"future round", uuid.New(), orgID, repoID, prID, cutoff.Add(time.Hour), &before, false},
		{"unfinished round", uuid.New(), orgID, repoID, prID, before, nil, false},
		{"completed after source", uuid.New(), orgID, repoID, prID, before, &after, false},
	}
	for _, tt := range cases {
		_, err = pool.Exec(ctx, `INSERT INTO code_review_session_metadata(session_id,org_id,repository_id,pull_request_id,policy_id,created_at,completed_at,final_review_body)VALUES($1,$2,$3,$4,$5,$6,$7,'Legacy report')`, tt.id, tt.org, tt.repo, tt.pr, policyID, tt.created, tt.completed)
		require.NoError(t, err, "seed %s", tt.name)
	}
	for _, status := range []models.CodeReviewSessionStatus{models.CodeReviewSessionStatusFailed, models.CodeReviewSessionStatusCancelled, models.CodeReviewSessionStatusStale} {
		id := uuid.New()
		_, err = pool.Exec(ctx, `INSERT INTO code_review_session_metadata(session_id,org_id,repository_id,pull_request_id,policy_id,status,created_at,completed_at)VALUES($1,$2,$3,$4,$5,$6,$7,$7)`, id, orgID, repoID, prID, policyID, status, before)
		require.NoError(t, err, "seed unsuccessful legacy review")
		t.Run("exclude "+string(status), func(t *testing.T) {
			t.Parallel()
			_, err := store.GetPriorRound(ctx, orgID, repoID, currentID, id)
			require.ErrorIs(t, err, pgx.ErrNoRows, "an unsuccessful legacy run must not be presented as a prior completed review")
		})
	}
	assessmentID, newerAssessmentID := uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO code_review_revision_assessments(id,org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,status,decision,completed_at,rendered_body,generation) VALUES($1,$3,$4,$5,$6,$7,'reviewed-base','reviewed-head','completed','blocked',$8,'Immutable earlier report',1),($2,$3,$4,$5,$6,$7,'future-base','future-head','completed','approved',$9,'Future report must remain hidden',2)`, assessmentID, newerAssessmentID, orgID, priorID, repoID, prID, policyID, before, cutoff.Add(time.Hour))
	require.NoError(t, err, "seed immutable assessment and later reassessment")
	_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET completed_at=$2,final_review_body='Mutated session report' WHERE session_id=$1`, priorID, cutoff.Add(time.Hour))
	require.NoError(t, err, "simulate a later recheck changing the session projection")
	findID, resultID := uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO code_review_findings(id,org_id,session_id,assessment_id,summary,body,created_at)VALUES($1,$2,$3,$4,'Earlier issue','Earlier evidence',$5),($6,$2,$3,$7,'Future issue','Future evidence',$5)`, findID, orgID, priorID, assessmentID, before, uuid.New(), newerAssessmentID)
	require.NoError(t, err, "seed assessment-bound findings")
	_, err = pool.Exec(ctx, `INSERT INTO code_review_agent_results(id,org_id,session_id,assessment_id,raw_output,created_at)VALUES($1,$2,$3,$4,'Earlier reviewer result',$5),($6,$2,$3,$7,'Future reviewer result',$5)`, resultID, orgID, priorID, assessmentID, before, uuid.New(), newerAssessmentID)
	require.NoError(t, err, "seed assessment-bound agent results")
	_, err = pool.Exec(ctx, `INSERT INTO code_review_prompt_records(org_id,session_id,record_key,role,content,created_at)VALUES($1,$2,'stored-reviewer-output','reviewer_output','Earlier reviewer result',$3)`, orgID, priorID, before)
	require.NoError(t, err, "seed out-of-line reviewer evidence")
	_, err = pool.Exec(ctx, `UPDATE code_review_agent_results SET raw_output='Output stored separately',structured_result='{"raw_record_key":"stored-reviewer-output"}' WHERE id=$1`, resultID)
	require.NoError(t, err, "link stored reviewer output")
	_, err = store.GetPriorRound(ctx, orgID, repoID, currentID, currentID)
	require.ErrorIs(t, err, pgx.ErrNoRows, "current round must never be its own historical evidence")
	index, err := store.ListPriorRounds(ctx, orgID, repoID, currentID, nil, 1)
	require.NoError(t, err, "real scoped query should return the newest eligible prior round")
	require.Equal(t, priorID, index[0].SessionID, "first page should use session chronology even after a later reassessment")
	next, err := store.ListPriorRounds(ctx, orgID, repoID, currentID, &priorID, 5)
	require.NoError(t, err, "real cursor query should continue to older rounds")
	require.Equal(t, []uuid.UUID{olderID}, priorHistorySessionIDs(next), "pagination should exclude all foreign, unfinished and newer rounds")
	report, err := store.GetPriorRound(ctx, orgID, repoID, currentID, priorID)
	require.NoError(t, err, "retrieve eligible immutable assessment")
	require.Equal(t, "Immutable earlier report", report.FinalReviewBody, "later session projection changes must not overwrite historical report evidence")
	require.Equal(t, &assessmentID, report.AssessmentID, "historical evidence should name its exact assessment")
	require.Equal(t, "reviewed-head", report.HeadSHA, "head provenance must come from the same immutable assessment")
	findings, err := store.ListPriorFindings(ctx, orgID, priorID, report.EvidenceAssessmentID, cutoff, 50)
	require.NoError(t, err, "read baseline findings")
	require.Equal(t, []CodeReviewHistoryFinding{{ID: findID, Severity: models.CodeReviewFindingSeverityHigh, Confidence: models.CodeReviewFindingConfidenceHigh, Summary: "Earlier issue", Body: "Earlier evidence", BodyRunes: 16}}, findings, "only findings from the selected immutable assessment should be read")
	results, err := store.ListPriorAgentResults(ctx, orgID, priorID, report.EvidenceAssessmentID, cutoff, 20)
	require.NoError(t, err, "read baseline reviewer results")
	require.Equal(t, []CodeReviewHistoryAgentResult{{ID: resultID, AgentProvider: "codex", Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted, Output: "Earlier reviewer result", OutputRunes: 23}}, results, "only selected assessment reviewer results should be read")
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := store.GetPriorRound(ctx, orgID, repoID, currentID, tt.id)
			if tt.allowed {
				require.NoError(t, err, "same-PR earlier completed evidence should be readable")
			} else {
				require.ErrorIs(t, err, pgx.ErrNoRows, "out-of-scope history must be indistinguishable from missing evidence")
			}
		})
	}
	authCases := []struct {
		name                       string
		org, repo, session, thread uuid.UUID
		allowed                    bool
	}{{"bound orchestrator", orgID, repoID, currentID, orchestratorID, true}, {"reviewer", orgID, repoID, currentID, reviewerID, false}, {"wrong org", uuid.New(), repoID, currentID, orchestratorID, false}, {"wrong repo", orgID, uuid.New(), currentID, orchestratorID, false}, {"wrong session", orgID, repoID, uuid.New(), orchestratorID, false}}
	for _, tt := range authCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := store.AuthorizeSource(ctx, tt.org, tt.repo, tt.session, tt.thread)
			if tt.allowed {
				require.NoError(t, err, "pre-dispatch orchestrator binding should authorize without an agent-result row")
			} else {
				require.ErrorIs(t, err, pgx.ErrNoRows, "unbound source identities must be rejected")
			}
		})
	}
}

func priorHistorySessionIDs(rounds []CodeReviewHistoryRound) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(rounds))
	for _, r := range rounds {
		out = append(out, r.SessionID)
	}
	return out
}

func TestCodeReviewHistoryPostgresMessageChunksAndReceipts(t *testing.T) {
	t.Parallel()
	pool := newPriorHistoryPostgres(t)
	ctx := context.Background()
	store := NewCodeReviewHistoryStore(pool)
	orgID, sessionID, threadID, otherThreadID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	cutoff := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	before := cutoff.Add(-time.Hour)
	_, err := pool.Exec(ctx, `INSERT INTO session_threads(id,org_id,session_id,created_at)VALUES($1,$3,$4,$5),($2,$3,$4,$5)`, threadID, otherThreadID, orgID, sessionID, before)
	require.NoError(t, err, "seed history threads")
	_, err = pool.Exec(ctx, `INSERT INTO session_messages(id,org_id,session_id,thread_id,content,created_at)VALUES(1,$1,$2,$3,$5,$6),(2,$1,$2,$4,'Sibling evidence',$6),(3,$1,$2,$3,'Future evidence',$7),(4,$1,$2,$3,'Next message',$6)`, orgID, sessionID, threadID, otherThreadID, strings.Repeat("é", 4005), before, cutoff.Add(time.Hour))
	require.NoError(t, err, "seed long and out-of-scope messages")
	first, err := store.ListPriorMessages(ctx, orgID, sessionID, threadID, cutoff, 0, 0, 10)
	require.NoError(t, err, "fetch first bounded message chunks")
	for i := range first {
		first[i].CreatedAt = first[i].CreatedAt.UTC()
	}
	require.Equal(t, []CodeReviewHistoryMessage{{ID: 1, Role: models.MessageRoleAssistant, TurnNumber: 1, Content: strings.Repeat("é", 4000), ContentRunes: 4005, CreatedAt: before}, {ID: 4, Role: models.MessageRoleAssistant, TurnNumber: 1, Content: "Next message", ContentRunes: 12, CreatedAt: before}}, first, "SQL must bound Unicode text while excluding sibling and future messages")
	rest, err := store.ListPriorMessages(ctx, orgID, sessionID, threadID, cutoff, 1, 4000, 10)
	require.NoError(t, err, "continue the long message")
	require.Equal(t, strings.Repeat("é", 5), rest[0].Content, "offset pagination must preserve every remaining rune")
	valid, err := store.ValidMessageCursor(ctx, orgID, sessionID, threadID, cutoff, 2, 0)
	require.NoError(t, err, "validate cursor ownership")
	require.False(t, valid, "a sibling thread's message cannot serve as this thread's cursor")
	metadata := json.RawMessage(`{"kind":"prior_messages"}`)
	original := json.RawMessage(`{"data":[{"content":"original"}]}`)
	actual, err := store.SaveReceipt(ctx, orgID, sessionID, "receipt", original, metadata)
	require.NoError(t, err, "persist retrieval receipt")
	require.JSONEq(t, string(original), string(actual), "first reader should receive recorded content")
	replayed, err := store.SaveReceipt(ctx, orgID, sessionID, "receipt", json.RawMessage(`{"data":[]}`), metadata)
	require.NoError(t, err, "repeat immutable retrieval receipt")
	require.JSONEq(t, string(original), string(replayed), "a repeated receipt must never replace evidence already shown")
}
