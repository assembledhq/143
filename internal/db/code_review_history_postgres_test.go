package db

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func newPriorHistoryPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	// The shared manager migrates the repository schema once, clones a database
	// per fixture, and releases setup connections before parallel tests run.
	return testpostgres.NewPool(t, context.Background(), "../../migrations", 2)
}

func seedPriorHistoryParents(t *testing.T, pool *pgxpool.Pool, orgID, repoID, prID, policyID uuid.UUID) {
	t.Helper()
	integrationID := uuid.NewSHA1(repoID, []byte("synthetic-history-integration"))
	for _, row := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO organizations(id,name) VALUES($1,'Synthetic review history') ON CONFLICT (id) DO NOTHING`, []any{orgID}},
		{`INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github') ON CONFLICT (id) DO NOTHING`, []any{integrationID, orgID}},
		{`INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,(hashtextextended($1::uuid::text,0)&9223372036854775807),'synthetic/history','https://example.invalid/history.git',1) ON CONFLICT (id) DO NOTHING`, []any{repoID, orgID, integrationID}},
		{`INSERT INTO pull_requests(id,org_id,github_pr_number,github_pr_url,github_repo,title) VALUES($1,$2,(hashtextextended($1::uuid::text,0)&2147483647),'https://example.invalid/history/pr','synthetic/history','Synthetic history') ON CONFLICT (id) DO NOTHING`, []any{prID, orgID}},
		{`INSERT INTO code_review_policies(id,org_id,version,approval_mode,description_policy,risk_policy,agent_roster,review_instructions,automated_approval_policy,continuation_policy) VALUES($1,$2,1,'comment_only','{}','{}','[]','','','{}') ON CONFLICT (id) DO NOTHING`, []any{policyID, orgID}},
	} {
		_, err := pool.Exec(context.Background(), row.query, row.args...)
		require.NoError(t, err, "seed real tenant, repository, pull-request and policy parents")
	}
}

func seedPriorHistoryRound(t *testing.T, pool *pgxpool.Pool, orgID, repoID, prID, policyID, sessionID uuid.UUID, createdAt time.Time, completedAt *time.Time, status models.CodeReviewSessionStatus) uuid.UUID {
	t.Helper()
	seedPriorHistoryParents(t, pool, orgID, repoID, prID, policyID)
	_, err := pool.Exec(context.Background(), `INSERT INTO sessions(id,org_id,repository_id,origin,status,created_at) VALUES($1,$2,$3,'code_review','completed',$4)`, sessionID, orgID, repoID, createdAt)
	require.NoError(t, err, "seed a real code-review session")
	metadataID := uuid.New()
	_, err = pool.Exec(context.Background(), `INSERT INTO code_review_session_metadata(id,org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,decision,review_output_key,created_at,completed_at,final_review_body) VALUES($1,$2,$3,$4,$5,$6,'base','head','slash_command',$7,'blocked',$3::uuid::text,$8,$9,'Legacy report')`, metadataID, orgID, sessionID, repoID, prID, policyID, status, createdAt, completedAt)
	require.NoError(t, err, "seed full-schema code-review metadata")
	return metadataID
}

func seedPriorHistoryAssessment(t *testing.T, pool *pgxpool.Pool, orgID, repoID, prID, policyID, sessionID, metadataID, assessmentID uuid.UUID, generation int, completedAt time.Time, body string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,generation,base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,input_digest,input_manifest,review_scope,route_reason,status,publication_key,completed_at,result_origin,coverage_complete,decision,acceptable,structured_outcome,rendered_body,publication_state) VALUES($1,$2,$3,'synthetic/history',$4,$5,$6,$7,$8,'reviewed-base','main','reviewed-head',1,'code','contract','intent','visual','request','gate',$1::uuid::text,'{}','full','initial_full','completed',$1::uuid::text,$9,'executed',true,'blocked',false,'{}',$10,'not_required')`, assessmentID, orgID, repoID, prID, metadataID, sessionID, policyID, generation, completedAt, body)
	require.NoError(t, err, "seed a completed immutable assessment with real foreign keys and outcome constraints")
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
	seedPriorHistoryRound(t, pool, orgID, repoID, prID, policyID, currentID, cutoff, nil, models.CodeReviewSessionStatusRunning)
	_, err := pool.Exec(ctx, `INSERT INTO session_threads(id,org_id,session_id,label,agent_type,created_at)VALUES($1,$3,$4,'Orchestrator','codex',$5),($2,$3,$4,'Reviewer','codex',$5)`, orchestratorID, reviewerID, orgID, currentID, cutoff)
	require.NoError(t, err, "seed independent reviewer and orchestrator threads")
	_, err = pool.Exec(ctx, `INSERT INTO code_review_prompt_records(org_id,session_id,record_key,role,content,metadata)VALUES($1,$2,'synthesis','orchestrator','Synthetic synthesis',jsonb_build_object('review_history_thread_id',$3::text))`, orgID, currentID, orchestratorID.String())
	require.NoError(t, err, "persist trusted pre-dispatch binding")
	cases := []struct {
		name                      string
		id, org, repo, pr, policy uuid.UUID
		created                   time.Time
		completed                 *time.Time
		allowed                   bool
	}{
		{"prior round", priorID, orgID, repoID, prID, policyID, before, &before, true},
		{"older round", olderID, orgID, repoID, prID, policyID, before.Add(-time.Hour), &before, true},
		{"other org", uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), before, &before, false},
		{"other repository", uuid.New(), orgID, uuid.New(), prID, policyID, before, &before, false},
		{"other pull request", uuid.New(), orgID, repoID, uuid.New(), policyID, before, &before, false},
		{"same-time round", uuid.New(), orgID, repoID, prID, policyID, cutoff, &before, false},
		{"future round", uuid.New(), orgID, repoID, prID, policyID, cutoff.Add(time.Hour), &before, false},
		{"unfinished round", uuid.New(), orgID, repoID, prID, policyID, before, nil, false},
		{"completed after source", uuid.New(), orgID, repoID, prID, policyID, before, &after, false},
	}
	var priorMetadataID uuid.UUID
	for _, tt := range cases {
		metadataID := seedPriorHistoryRound(t, pool, tt.org, tt.repo, tt.pr, tt.policy, tt.id, tt.created, tt.completed, models.CodeReviewSessionStatusCompleted)
		if tt.id == priorID {
			priorMetadataID = metadataID
		}
	}
	for _, status := range []models.CodeReviewSessionStatus{models.CodeReviewSessionStatusFailed, models.CodeReviewSessionStatusCancelled, models.CodeReviewSessionStatusStale} {
		id := uuid.New()
		seedPriorHistoryRound(t, pool, orgID, repoID, prID, policyID, id, before, &before, status)
		t.Run("exclude "+string(status), func(t *testing.T) {
			t.Parallel()
			_, err := store.GetPriorRound(ctx, orgID, repoID, currentID, id)
			require.ErrorIs(t, err, pgx.ErrNoRows, "an unsuccessful legacy run must not be presented as a prior completed review")
		})
	}
	assessmentID, newerAssessmentID := uuid.New(), uuid.New()
	seedPriorHistoryAssessment(t, pool, orgID, repoID, prID, policyID, priorID, priorMetadataID, assessmentID, 1, before, "Immutable earlier report")
	seedPriorHistoryAssessment(t, pool, orgID, repoID, prID, policyID, priorID, priorMetadataID, newerAssessmentID, 2, cutoff.Add(time.Hour), "Future report must remain hidden")
	_, err = pool.Exec(ctx, `UPDATE code_review_session_metadata SET completed_at=$2,final_review_body='Mutated session report' WHERE session_id=$1`, priorID, cutoff.Add(time.Hour))
	require.NoError(t, err, "simulate a later recheck changing the session projection")
	findID, resultID := uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO code_review_findings(id,org_id,session_id,assessment_id,dedupe_key,severity,confidence,summary,body,created_at)VALUES($1,$2,$3,$4,'earlier','high','high','Earlier issue','Earlier evidence',$5),($6,$2,$3,$7,'future','high','high','Future issue','Future evidence',$5)`, findID, orgID, priorID, assessmentID, before, uuid.New(), newerAssessmentID)
	require.NoError(t, err, "seed assessment-bound findings")
	_, err = pool.Exec(ctx, `INSERT INTO code_review_agent_results(id,org_id,session_id,assessment_id,agent_provider,role,status,raw_output,created_at)VALUES($1,$2,$3,$4,'codex','reviewer','completed','Earlier reviewer result',$5),($6,$2,$3,$7,'codex','reviewer','completed','Future reviewer result',$5)`, resultID, orgID, priorID, assessmentID, before, uuid.New(), newerAssessmentID)
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
	orgID, repoID, prID, policyID, sessionID, threadID, otherThreadID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	cutoff := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	before := cutoff.Add(-time.Hour)
	seedPriorHistoryRound(t, pool, orgID, repoID, prID, policyID, sessionID, before, &before, models.CodeReviewSessionStatusCompleted)
	_, err := pool.Exec(ctx, `INSERT INTO session_threads(id,org_id,session_id,label,agent_type,created_at)VALUES($1,$3,$4,'Main','codex',$5),($2,$3,$4,'Sibling','codex',$5)`, threadID, otherThreadID, orgID, sessionID, before)
	require.NoError(t, err, "seed history threads")
	_, err = pool.Exec(ctx, `INSERT INTO session_messages(id,org_id,session_id,thread_id,role,turn_number,content,created_at)VALUES(1,$1,$2,$3,'assistant',1,$5,$6),(2,$1,$2,$4,'assistant',1,'Sibling evidence',$6),(3,$1,$2,$3,'assistant',1,'Future evidence',$7),(4,$1,$2,$3,'assistant',1,'Next message',$6)`, orgID, sessionID, threadID, otherThreadID, strings.Repeat("é", 4005), before, cutoff.Add(time.Hour))
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

func TestCodeReviewHistoryPromptRoleMigrationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		legacy bool
	}{
		{name: "current schema"},
		{name: "current and synchronized legacy schema", legacy: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newPriorHistoryPostgres(t)
			ctx := context.Background()
			orgID, repoID, prID, policyID, sessionID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			at := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
			seedPriorHistoryRound(t, pool, orgID, repoID, prID, policyID, sessionID, at, &at, models.CodeReviewSessionStatusCompleted)
			if tt.legacy {
				// Historical 225 used artifact names. Recreate its actual table
				// contract, including FKs, before installing the compatibility trigger.
				legacy, err := os.ReadFile("../../migrations/000225_code_review_prompt_records.up.sql")
				require.NoError(t, err, "read the historical prompt table migration")
				legacySQL := strings.ReplaceAll(strings.ReplaceAll(string(legacy), "prompt_records", "prompt_artifacts"), "record_key", "artifact_key")
				_, err = pool.Exec(ctx, legacySQL)
				require.NoError(t, err, "install the real historical prompt table with legacy names")
				applyPriorHistoryMigration(t, pool, "000288_fix_code_review_prompt_record_compatibility.up.sql")
				_, err = pool.Exec(ctx, `CREATE TRIGGER trg_sync_code_review_prompt_artifacts AFTER INSERT OR UPDATE OR DELETE ON code_review_prompt_artifacts FOR EACH ROW EXECUTE FUNCTION sync_code_review_prompt_record_compatibility();
 CREATE TRIGGER trg_sync_code_review_prompt_records AFTER INSERT OR UPDATE OR DELETE ON code_review_prompt_records FOR EACH ROW EXECUTE FUNCTION sync_code_review_prompt_record_compatibility()`)
				require.NoError(t, err, "install the existing bidirectional rolling-deploy compatibility triggers")
			}
			// The old live contract must reproduce the bug even though this fixture
			// starts from the fully migrated repository schema.
			applyPriorHistoryMigration(t, pool, "000286_code_review_visual_evidence_prompt_role.up.sql")
			store := NewCodeReviewHistoryStore(pool)
			content := json.RawMessage(`{"data":{"report":"synthetic prior round"}}`)
			metadata := json.RawMessage(`{"kind":"prior_round"}`)
			_, err := store.SaveReceipt(ctx, orgID, sessionID, "before-migration", content, metadata)
			var rejected *pgconn.PgError
			require.ErrorAs(t, err, &rejected, "the prior schema must reject the exact receipt write used by history retrieval")
			require.Equal(t, "23514", rejected.Code, "the original failure must be the role check constraint")

			applyPriorHistoryMigration(t, pool, "000396_code_review_history_prompt_role.up.sql")
			actual, err := store.SaveReceipt(ctx, orgID, sessionID, "after-migration", content, metadata)
			require.NoError(t, err, "the new schema must accept history receipts through the real store")
			require.JSONEq(t, string(content), string(actual), "the accepted receipt must preserve the returned historical evidence")
			if tt.legacy {
				var mirrored string
				err = pool.QueryRow(ctx, `SELECT content FROM code_review_prompt_artifacts WHERE org_id=$1 AND artifact_key='after-migration'`, orgID).Scan(&mirrored)
				require.NoError(t, err, "the synchronized legacy role constraint must accept new receipts too")
				require.JSONEq(t, string(content), mirrored, "both worker generations must retain identical receipt bytes")
				_, err = pool.Exec(ctx, `INSERT INTO code_review_prompt_artifacts(org_id,session_id,artifact_key,role,content) VALUES($1,$2,'legacy-write','review_history',$3)`, orgID, sessionID, string(content))
				require.NoError(t, err, "the widened legacy table must synchronize history receipts back to current records")
				legacyReceipt, err := store.GetReceipt(ctx, orgID, sessionID, "legacy-write")
				require.NoError(t, err, "current readers must observe a receipt synchronized from legacy writers")
				require.JSONEq(t, string(content), string(legacyReceipt), "reverse synchronization must preserve receipt evidence")
			}

			applyPriorHistoryMigration(t, pool, "000396_code_review_history_prompt_role.down.sql")
			retained, err := store.GetReceipt(ctx, orgID, sessionID, "after-migration")
			require.NoError(t, err, "rollback must preserve readable audit receipts instead of deleting them")
			require.JSONEq(t, string(content), string(retained), "rollback must keep the exact evidence previously shown")
			_, err = store.SaveReceipt(ctx, orgID, sessionID, "after-rollback", content, metadata)
			require.ErrorAs(t, err, &rejected, "rollback should restore the old contract for new history writes")
			require.Equal(t, "23514", rejected.Code, "new writes must be rejected without invalidating existing audit rows")
			if tt.legacy {
				var retainedRows int
				err = pool.QueryRow(ctx, `SELECT count(*) FROM code_review_prompt_artifacts WHERE org_id=$1 AND role='review_history'`, orgID).Scan(&retainedRows)
				require.NoError(t, err, "rollback must retain synchronized legacy audit rows")
				require.Equal(t, 2, retainedRows, "both successfully synchronized receipts must survive rollback")
				_, err = pool.Exec(ctx, `INSERT INTO code_review_prompt_artifacts(org_id,session_id,artifact_key,role,content) VALUES($1,$2,'legacy-after-rollback','review_history',$3)`, orgID, sessionID, string(content))
				require.ErrorAs(t, err, &rejected, "rollback must also narrow legacy writes")
				require.Equal(t, "23514", rejected.Code, "the legacy role constraint must reject new history receipts after rollback")
			}
			applyPriorHistoryMigration(t, pool, "000396_code_review_history_prompt_role.up.sql")
			_, err = store.SaveReceipt(ctx, orgID, sessionID, "after-reapply", content, metadata)
			require.NoError(t, err, "reapplying the migration must validate retained audit rows and restore receipt writes")
		})
	}
}

func applyPriorHistoryMigration(t *testing.T, pool *pgxpool.Pool, file string) {
	t.Helper()
	body, err := os.ReadFile("../../migrations/" + file)
	require.NoError(t, err, "read the tested repository migration")
	_, err = pool.Exec(context.Background(), string(body))
	require.NoError(t, err, "apply the real migration %s to the isolated fixture", file)
}
