package worker

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func codeReviewFindingsPostgresConn(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL for persisted finding reconciliation proof")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err, "connect to disposable PostgreSQL for finding reconciliation")
	t.Cleanup(func() {
		require.NoError(t, conn.Close(context.Background()), "close the isolated temporary-table connection")
	})
	_, err = conn.Exec(ctx, `
		CREATE TEMP TABLE code_review_findings (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(), org_id uuid NOT NULL, session_id uuid NOT NULL,
			agent_result_id uuid, dedupe_key text NOT NULL, severity text NOT NULL, confidence text NOT NULL,
			path text, start_line integer, end_line integer, summary text NOT NULL, body text NOT NULL,
			selected_for_inline boolean NOT NULL DEFAULT false, github_comment_id bigint,
			created_at timestamptz NOT NULL DEFAULT now(), UNIQUE (org_id, session_id, dedupe_key)
		);
		CREATE TEMP TABLE code_review_agent_results (
			id uuid PRIMARY KEY, org_id uuid NOT NULL, session_id uuid NOT NULL,
			agent_provider text NOT NULL, agent_model text, role text NOT NULL, status text NOT NULL,
			raw_output text, structured_result jsonb, created_at timestamptz NOT NULL DEFAULT now()
		);
		CREATE TEMP TABLE code_review_prompt_records (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(), org_id uuid NOT NULL, session_id uuid NOT NULL,
			record_key text NOT NULL, role text NOT NULL, agent_provider text NOT NULL, content text NOT NULL,
			metadata jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now(), UNIQUE (org_id, record_key)
		);`)
	require.NoError(t, err, "create connection-local tables matching the finding, result, and prompt-record contracts")
	return conn
}

func TestCodeReviewSynthesisRepairReconcilesPersistedFindingsPostgres(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		oversized bool
	}{
		{name: "inline original response"},
		{name: "original response stored by reference", oversized: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			conn := codeReviewFindingsPostgresConn(t)
			orgID, sessionID, resultID := uuid.New(), uuid.New(), uuid.New()
			job := runCodeReviewPayload{OrgID: orgID, SessionID: sessionID}
			store := db.NewCodeReviewStore(conn)
			stores := &Stores{CodeReviews: store}
			state := codeReviewOrchestratorStructuredResult{ThreadID: uuid.NewString()}
			_, err := conn.Exec(ctx, `INSERT INTO code_review_agent_results(id,org_id,session_id,agent_provider,role,status,structured_result)
				VALUES($1,$2,$3,'codex','orchestrator','running',$4)`, resultID, orgID, sessionID, marshalCodeReviewOrchestratorStructuredResult(state))
			require.NoError(t, err, "seed the running orchestrator that will request a synthesis repair")
			const directive = `::code-comment{title="[P1] Scope cached lookups to the tenant" body="[P1] A missing tenant filter exposes another tenant's cache entries." file="internal/db/cache.go" start=42 end=42 priority=1}`
			original := directive + "\n```json\n{}\n```"
			if tt.oversized {
				original += strings.Repeat("x", codeReviewRawOutputInlineLimit)
			}
			sender := &codeReviewOrchestratorRepairSenderStub{}
			handled, started, err := requestCodeReviewOrchestratorSynthesisRepair(ctx, stores, sender, zerolog.Nop(), job,
				models.DefaultCodeReviewPolicyConfig(), []codereviewsvc.PullRequestFile{{Filename: "internal/db/cache.go"}},
				models.CodeReviewAgentResult{ID: resultID, AgentProvider: "codex", Role: models.CodeReviewAgentRoleOrchestrator},
				state, 1, original, errors.New("missing synthesis fields"), models.CodeReviewVisualEvidenceSnapshot{})
			require.NoError(t, err, "malformed synthesis should persist its finding before requesting repair")
			require.Equal(t, []bool{true, true}, []bool{handled, started}, "the repair should be handled and started")
			results, err := store.ListAgentResults(ctx, orgID, sessionID)
			require.NoError(t, err, "read the durable repair checkpoint")
			require.Equal(t, 1, len(results), "the fixture should retain its single orchestrator result")
			state, ok := parseCodeReviewOrchestratorStructuredResult(results[0].StructuredResult)
			require.True(t, ok, "the saved repair checkpoint should decode")
			require.Equal(t, tt.oversized, state.RawRecordKey != "", "oversized output should be stored by reference")
			state = codeReviewOrchestratorObserveRepairCompletion(state, 2)
			combined, err := codeReviewOrchestratorCombinedOutputFromRecord(ctx, store, orgID, results[0].RawOutput, state, "Repaired synthesis.")
			require.NoError(t, err, "repair should resolve the full original output before matching findings")
			require.Contains(t, combined, directive, "even a record-backed original should contribute its directive evidence")
			synthesis := codeReviewOrchestratorSynthesis{Findings: []codeReviewOrchestratorFinding{{
				Severity: models.CodeReviewFindingSeverityHigh, Confidence: models.CodeReviewFindingConfidenceHigh,
				Path: stringPtr("internal/db/cache.go"), StartLine: intPtr(42), EndLine: intPtr(42),
				Summary: "Missing tenant filter", Body: "The cache lookup must include the tenant ID.",
			}}}
			expected := codeReviewFindingsFromSynthesis(synthesis, []string{"internal/db/cache.go"})
			expected[0].OrgID, expected[0].SessionID, expected[0].AgentResultID = orgID, sessionID, &resultID
			for attempt := 0; attempt < 2; attempt++ {
				count, err := persistCodeReviewOrchestratorFindings(ctx, stores, job, resultID, &synthesis, combined, []string{"internal/db/cache.go"})
				require.NoError(t, err, "valid repaired synthesis and its retry should reconcile the persisted finding")
				require.Equal(t, 1, count, "one repaired issue should count once")
				actual, err := store.ListFindings(ctx, orgID, sessionID, false)
				require.NoError(t, err, "read the actual findings used to build the final review")
				body := models.BuildCodeReviewFinalReviewBody(models.CodeReviewFinalReviewInput{Findings: actual})
				require.Equal(t, 1, strings.Count(body, "Missing tenant filter"), "the saved issue should render once after repair and on retry")
				require.NotContains(t, body, "Scope cached lookups to the tenant", "the superseded repair row should not remain in the rendered review")
				for i := range actual {
					actual[i].ID, actual[i].CreatedAt = uuid.Nil, time.Time{}
				}
				require.Equal(t, expected, actual, "the persisted set should contain exactly the canonical issue with its tenant, session, attribution, and content")
			}
		})
	}
}

func TestCodeReviewFindingCleanupProtectsOtherScopesAndReceiptsPostgres(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := codeReviewFindingsPostgresConn(t)
	store := db.NewCodeReviewStore(conn)
	orgID, sessionID, resultID := uuid.New(), uuid.New(), uuid.New()
	otherOrg, otherSession, otherResult := uuid.New(), uuid.New(), uuid.New()
	postedID := int64(123)
	fixtures := []models.CodeReviewFinding{
		{OrgID: orgID, SessionID: sessionID, AgentResultID: &resultID, DedupeKey: "old-key"},
		{OrgID: otherOrg, SessionID: sessionID, AgentResultID: &resultID, DedupeKey: "old-key"},
		{OrgID: orgID, SessionID: otherSession, AgentResultID: &resultID, DedupeKey: "old-key"},
		{OrgID: orgID, SessionID: sessionID, AgentResultID: &otherResult, DedupeKey: "peer-key"},
		{OrgID: orgID, SessionID: sessionID, AgentResultID: &resultID, DedupeKey: "posted-key", GitHubCommentID: &postedID},
	}
	for i := range fixtures {
		fixtures[i].Severity, fixtures[i].Confidence = models.CodeReviewFindingSeverityHigh, models.CodeReviewFindingConfidenceHigh
		fixtures[i].Summary, fixtures[i].Body = "Fixture issue", "Synthetic cleanup boundary proof."
		require.NoError(t, store.CreateFinding(ctx, &fixtures[i]), "seed a finding in each independent cleanup scope")
	}
	require.NoError(t, store.DeleteUnpostedFindings(ctx, orgID, sessionID, resultID, []string{"old-key", "peer-key", "posted-key"}), "remove only the requested unposted finding in the owning attempt")
	for i, fixture := range fixtures {
		var exists bool
		err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM code_review_findings WHERE org_id=$1 AND id=$2)`, fixture.OrgID, fixture.ID).Scan(&exists)
		require.NoError(t, err, "read back each cleanup boundary")
		require.Equal(t, i != 0, exists, "cleanup should retain every other tenant, session, reviewer, and posted receipt")
	}
}
