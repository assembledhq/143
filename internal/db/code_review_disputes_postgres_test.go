package db

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewDisputeStore_SetTriagePostgres(t *testing.T) {
	t.Parallel()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL for dispute triage array and update fence proof")
	}
	tests := []struct {
		name            string
		input, expected []models.CodeReviewRiskReasonCode
		intakeStatus    models.CodeReviewDisputeIntakeStatus
		foreignOrg      bool
	}{
		{name: "nil reasons", expected: []models.CodeReviewRiskReasonCode{}, intakeStatus: models.CodeReviewDisputeIntakePending},
		{name: "empty reasons", input: []models.CodeReviewRiskReasonCode{}, expected: []models.CodeReviewRiskReasonCode{}, intakeStatus: models.CodeReviewDisputeIntakePending},
		{name: "populated reasons", input: []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonDescriptionFailed, models.CodeReviewRiskReasonBlockingFindings}, expected: []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonDescriptionFailed, models.CodeReviewRiskReasonBlockingFindings}, intakeStatus: models.CodeReviewDisputeIntakePending},
		{name: "other tenant", intakeStatus: models.CodeReviewDisputeIntakePending, foreignOrg: true},
		{name: "triaged dispute", intakeStatus: models.CodeReviewDisputeIntakeTriaged},
		{name: "discarded dispute", intakeStatus: models.CodeReviewDisputeIntakeDiscarded},
		{name: "failed dispute", intakeStatus: models.CodeReviewDisputeIntakeFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			tx, orgID, disputeID := newDisputeTriagePostgres(t, databaseURL, tt.intakeStatus)
			store := NewCodeReviewDisputeStore(tx)
			before, err := store.GetByID(ctx, orgID, disputeID)
			require.NoError(t, err, "read the complete dispute before triage")
			result := models.CodeReviewDisputeTriageResult{
				Direction: models.CodeReviewDisputeDirectionShouldHaveApproved, ContestedReasonCodes: slices.Clone(tt.input),
				DisputeKind: "new_evidence", AssertsNewInformation: true,
				Routing: models.CodeReviewDisputeRoutingPolicySignalOnly, Confidence: .99,
			}
			queryOrgID := orgID
			if tt.foreignOrg {
				queryOrgID = uuid.New()
			}
			actual, err := store.SetTriage(ctx, queryOrgID, disputeID, result, true, "Recorded.")
			if tt.foreignOrg || tt.intakeStatus != models.CodeReviewDisputeIntakePending {
				require.ErrorIs(t, err, pgx.ErrNoRows, "only the owning tenant's pending dispute may be triaged")
				after, readErr := store.GetByID(ctx, orgID, disputeID)
				require.NoError(t, readErr, "read the protected dispute after the rejected update")
				require.Equal(t, before, after, "a failed tenant or pending-state fence must leave every field unchanged")
				return
			}
			require.NoError(t, err, "PostgreSQL should accept nil, empty and populated reason inputs without a NOT NULL violation")
			expected := before
			expected.Direction = &result.Direction
			expected.ContestedReasonCodes = tt.expected
			expected.DisputeKind = &result.DisputeKind
			expected.AssertsNewInformation = &result.AssertsNewInformation
			expected.Routing = &result.Routing
			expected.IntakeStatus = models.CodeReviewDisputeIntakeTriaged
			expected.IntakeConfidence = &result.Confidence
			adjudicationStatus := models.CodeReviewDisputeAdjudicationPending
			expected.AdjudicationStatus = &adjudicationStatus
			detail := "Recorded."
			expected.StatusDetail = &detail
			expected.Version++
			// now() is stable within the isolated transaction, so updated_at
			// remains equal to the fixture's default created_at/updated_at.
			require.Equal(t, expected, actual, "triage should persist exactly one state transition and a nonnull reason array")
			require.Equal(t, tt.input, result.ContestedReasonCodes, "the store must not mutate the caller's nil or populated slice")
			persisted, err := store.GetByID(ctx, orgID, disputeID)
			require.NoError(t, err, "read the persisted triage")
			require.Equal(t, expected, persisted, "the returned triage must match the stored dispute")

			_, err = store.SetTriage(ctx, orgID, disputeID, result, true, "Retry must not overwrite the first triage.")
			require.ErrorIs(t, err, pgx.ErrNoRows, "a repeated triage must be fenced after the first pending-state transition")
			afterRetry, err := store.GetByID(ctx, orgID, disputeID)
			require.NoError(t, err, "read triage after retry")
			require.Equal(t, persisted, afterRetry, "retry must not rewrite reasons, detail, timestamps or version")
		})
	}
}

func newDisputeTriagePostgres(t *testing.T, databaseURL string, intakeStatus models.CodeReviewDisputeIntakeStatus) (pgx.Tx, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err, "connect to disposable PostgreSQL")
	t.Cleanup(func() { require.NoError(t, conn.Close(ctx), "close the isolated database connection") })
	tx, err := conn.Begin(ctx)
	require.NoError(t, err, "begin isolated dispute schema transaction")
	t.Cleanup(func() { require.NoError(t, tx.Rollback(ctx), "roll back the isolated dispute schema and fixtures") })
	schema := "dispute_triage_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = tx.Exec(ctx, `CREATE SCHEMA `+schema+`; SET LOCAL search_path TO `+schema+`,public`)
	require.NoError(t, err, "create an isolated schema for each parallel case")
	_, err = tx.Exec(ctx, `
CREATE TABLE organizations (id uuid PRIMARY KEY);
CREATE TABLE users (id uuid PRIMARY KEY);
CREATE TABLE sessions (id uuid PRIMARY KEY, org_id uuid NOT NULL);
CREATE TABLE repositories (id uuid PRIMARY KEY, org_id uuid NOT NULL);
CREATE TABLE pull_requests (id uuid PRIMARY KEY, org_id uuid NOT NULL);
CREATE TABLE code_review_policies (id uuid PRIMARY KEY, org_id uuid NOT NULL);
CREATE TABLE code_review_session_metadata (
    org_id uuid NOT NULL,
    trigger_source text CONSTRAINT chk_code_review_session_metadata_trigger_source CHECK (trigger_source IN ('app_reviewer'))
);
CREATE TABLE review_comments (id uuid PRIMARY KEY);`)
	require.NoError(t, err, "create the minimal parent tables required by the real dispute migrations")
	for _, migration := range []string{
		"000281_code_review_decision_disputes.up.sql",
		"000283_code_review_decision_outcomes.up.sql",
		"000284_code_review_dispute_review_request_route.up.sql",
	} {
		up, readErr := os.ReadFile(filepath.Join("..", "..", "migrations", migration))
		require.NoError(t, readErr, "read the real dispute migration %s", migration)
		_, err = tx.Exec(ctx, string(up))
		require.NoError(t, err, "apply the real dispute migration %s", migration)
	}
	orgID, sessionID, repositoryID, pullRequestID, policyID, disputeID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	seed := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id) VALUES ($1)`, []any{orgID}},
		{`INSERT INTO sessions (id, org_id) VALUES ($1, $2)`, []any{sessionID, orgID}},
		{`INSERT INTO repositories (id, org_id) VALUES ($1, $2)`, []any{repositoryID, orgID}},
		{`INSERT INTO pull_requests (id, org_id) VALUES ($1, $2)`, []any{pullRequestID, orgID}},
		{`INSERT INTO code_review_policies (id, org_id) VALUES ($1, $2)`, []any{policyID, orgID}},
		{`INSERT INTO code_review_decision_disputes (
    id, org_id, session_id, pull_request_id, repository_id, policy_id,
    reviewed_head_sha, decision, source, source_body_hash, body, semantic_input_hash_at_filing,
    direction, routing, asserts_new_information, intake_status
) VALUES ($1, $2, $3, $4, $5, $6, 'test-head', 'blocked', 'github_comment', 'test-body-hash',
    'Please reconsider the evidence.', 'test-input-hash', 'should_have_approved', 'answer_only', false, $7)`,
			[]any{disputeID, orgID, sessionID, pullRequestID, repositoryID, policyID, intakeStatus}},
	}
	for _, row := range seed {
		_, err = tx.Exec(ctx, row.sql, row.args...)
		require.NoError(t, err, "seed the tenant-owned dispute and its parents")
	}
	return tx, orgID, disputeID
}
