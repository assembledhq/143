package db

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

const (
	riskTierPromptRoleUp   = "../../migrations/000310_code_review_risk_tier_prompt_role.up.sql"
	riskTierPromptRoleDown = "../../migrations/000310_code_review_risk_tier_prompt_role.down.sql"
)

func TestCodeReviewRiskTierPromptRoleMigrationShape(t *testing.T) {
	t.Parallel()
	upBody, err := os.ReadFile(riskTierPromptRoleUp)
	require.NoError(t, err, "test should read the risk-tier prompt-role migration")
	upSQL := string(upBody)
	require.Equal(t, 2, strings.Count(upSQL, "'risk_tier_signal'"), "migration should widen both synchronized prompt tables")
	require.Equal(t, 2, strings.Count(upSQL, "'visual_evidence'"), "migration should keep the previously admitted visual-evidence role")
	require.Contains(t, upSQL, "to_regclass('code_review_prompt_artifacts')", "migration should detect installations still draining the legacy prompt table")

	downBody, err := os.ReadFile(riskTierPromptRoleDown)
	require.NoError(t, err, "test should read the risk-tier prompt-role rollback")
	downSQL := string(downBody)
	require.NotContains(t, downSQL, "'risk_tier_signal'", "rollback write contract should return to the prior prompt roles")
	require.Equal(t, 2, strings.Count(downSQL, ")) NOT VALID"), "rollback should preserve historical risk-tier checkpoints on both tables while narrowing new writes")
}

func TestCodeReviewRiskTierPromptRoleMigrationPostgres(t *testing.T) {
	t.Parallel()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL for the risk-tier prompt-role migration proof")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	require.NoError(t, err, "connect disposable PostgreSQL")
	defer func() { require.NoError(t, conn.Close(ctx), "close test database") }()
	schema := "risk_tier_prompt_role_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = conn.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "create isolated schema")
	defer func() {
		_, err := conn.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err, "remove isolated schema")
	}()
	_, err = conn.Exec(ctx, `SET search_path TO `+schema)
	require.NoError(t, err, "select isolated schema")
	_, err = conn.Exec(ctx, `
		CREATE TABLE code_review_prompt_records(org_id uuid NOT NULL, record_key text NOT NULL, role text NOT NULL,
			CONSTRAINT chk_code_review_prompt_records_role CHECK (role IN ('reviewer','orchestrator','description_policy','reviewer_output','orchestrator_output','visual_evidence')));
		CREATE TABLE code_review_prompt_artifacts(org_id uuid NOT NULL, artifact_key text NOT NULL, role text NOT NULL,
			CONSTRAINT chk_code_review_prompt_artifacts_role CHECK (role IN ('reviewer','orchestrator','description_policy','reviewer_output','orchestrator_output','visual_evidence')));`)
	require.NoError(t, err, "create the prompt tables with the current role contract")
	insert := func(table, keyColumn, key, role string) error {
		_, err := conn.Exec(ctx, `INSERT INTO `+table+`(org_id,`+keyColumn+`,role) VALUES($1,$2,$3)`, uuid.New(), key, role)
		return err
	}
	require.Error(t, insert("code_review_prompt_records", "record_key", "before", "risk_tier_signal"), "the current contract should reject risk-tier checkpoints")

	up, err := os.ReadFile(riskTierPromptRoleUp)
	require.NoError(t, err, "read the risk-tier prompt-role migration")
	_, err = conn.Exec(ctx, string(up))
	require.NoError(t, err, "apply the risk-tier prompt-role migration")
	require.NoError(t, insert("code_review_prompt_records", "record_key", "checkpoint", "risk_tier_signal"), "the migrated records table should admit risk-tier checkpoints")
	require.NoError(t, insert("code_review_prompt_artifacts", "artifact_key", "checkpoint", "risk_tier_signal"), "the migrated legacy table should admit risk-tier checkpoints")
	require.NoError(t, insert("code_review_prompt_records", "record_key", "visual", "visual_evidence"), "the migration should keep admitting visual evidence")

	down, err := os.ReadFile(riskTierPromptRoleDown)
	require.NoError(t, err, "read the risk-tier prompt-role rollback")
	_, err = conn.Exec(ctx, string(down))
	require.NoError(t, err, "rollback should succeed while historical risk-tier checkpoints exist")
	require.Error(t, insert("code_review_prompt_records", "record_key", "after", "risk_tier_signal"), "rollback should stop new risk-tier checkpoints")
	var retained int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM code_review_prompt_records WHERE role='risk_tier_signal'`).Scan(&retained), "count retained checkpoints")
	require.Equal(t, 1, retained, "rollback should retain historical risk-tier checkpoints as audit data")
}

func TestCodeReviewStore_HasPriorDeterministicEarlyStopIgnoresInformationalReasonsPostgres(t *testing.T) {
	t.Parallel()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL for the prior early-stop proof")
	}
	tests := []struct {
		name     string
		reasons  string
		expected bool
	}{
		{name: "stable blockers only", reasons: `[{"code":"files_limit_exceeded","actual":6,"limit":5}]`, expected: true},
		{name: "stable blockers with an applied size safeguard record", reasons: `[{"code":"files_limit_exceeded","actual":6,"limit":5},{"code":"size_safeguard","size_safeguard":{"outcome":"applied","source":"missing","head_sha":"head"}}]`, expected: true},
		{name: "stable blocker with a waived size safeguard record", reasons: `[{"code":"size_safeguard","size_safeguard":{"outcome":"waived","source":"authoritative","tier":"low","head_sha":"head"}},{"code":"blocked_path","subject":"deploy/prod.yml"}]`, expected: true},
		{name: "mutable blocker still requires a full review", reasons: `[{"code":"blocked_path","subject":"deploy/prod.yml"},{"code":"checks_failing"},{"code":"size_safeguard","size_safeguard":{"outcome":"waived","source":"authoritative","tier":"low","head_sha":"head"}}]`, expected: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			conn, err := pgx.Connect(ctx, url)
			require.NoError(t, err, "connect disposable PostgreSQL")
			defer func() { require.NoError(t, conn.Close(ctx), "close test database") }()
			schema := "risk_tier_early_stop_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			_, err = conn.Exec(ctx, `CREATE SCHEMA `+schema)
			require.NoError(t, err, "create isolated schema")
			defer func() {
				_, err := conn.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
				require.NoError(t, err, "remove isolated schema")
			}()
			_, err = conn.Exec(ctx, `SET search_path TO `+schema)
			require.NoError(t, err, "select isolated schema")
			_, err = conn.Exec(ctx, `
				CREATE TABLE code_review_policies(id uuid PRIMARY KEY, org_id uuid NOT NULL, risk_policy jsonb NOT NULL);
				CREATE TABLE code_review_session_metadata(org_id uuid NOT NULL, session_id uuid NOT NULL, pull_request_id uuid NOT NULL, policy_id uuid NOT NULL,
					head_sha text NOT NULL, status text NOT NULL, risk_reason_details jsonb);
				CREATE TABLE code_review_agent_results(org_id uuid NOT NULL, session_id uuid NOT NULL);`)
			require.NoError(t, err, "create minimal early-stop tables")
			orgID, policyID, pullRequestID, priorSessionID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			_, err = conn.Exec(ctx, `INSERT INTO code_review_policies VALUES($1,$2,'{"stop_after_deterministic_failure":true}')`, policyID, orgID)
			require.NoError(t, err, "seed early-stop policy")
			_, err = conn.Exec(ctx, `INSERT INTO code_review_session_metadata VALUES($1,$2,$3,$4,'head','completed',$5::jsonb)`, orgID, priorSessionID, pullRequestID, policyID, tt.reasons)
			require.NoError(t, err, "seed prior completed review")
			exists, err := NewCodeReviewStore(conn).HasPriorDeterministicEarlyStop(ctx, orgID, pullRequestID, uuid.New(), "head")
			require.NoError(t, err, "prior early-stop lookup should execute against PostgreSQL")
			require.Equal(t, tt.expected, exists, "informational safeguard records should not hide or invent a prior deterministic early stop")
		})
	}
}

func TestCodeReviewStore_GetRiskReasonCodesBySessionOmitsInformationalReasons(t *testing.T) {
	t.Parallel()
	orgID, sessionID := uuid.New(), uuid.New()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()
	mock.ExpectQuery("(?s)SELECT reason->>'code'.*FROM code_review_session_metadata metadata.*metadata.org_id = @org_id").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).
		WillReturnRows(pgxmock.NewRows([]string{"code"}).AddRow("blocked_path").AddRow("retired_code").AddRow("size_safeguard"))

	codes, err := NewCodeReviewStore(mock).GetRiskReasonCodesBySession(context.Background(), orgID, sessionID)

	require.NoError(t, err, "reason code lookup should succeed")
	require.Equal(t, []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonBlockedPath}, codes, "disputes should see only blocking reason codes")
	require.NoError(t, mock.ExpectationsWereMet(), "reason code lookup should stay org and session scoped")
}
