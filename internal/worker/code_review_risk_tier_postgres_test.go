package worker

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

func TestResolveCodeReviewRiskTierSignalCheckpointPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for the risk-tier checkpoint proof")
	}
	ctx := context.Background()
	pool := fullRecoveryPostgresPool(t, ctx)
	org, session := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Risk Tier Checkpoint Test')`, org)
	require.NoError(t, err, "seed organization")
	_, err = pool.Exec(ctx, `INSERT INTO sessions(id,org_id,origin,status) VALUES($1,$2,'code_review','idle')`, session, org)
	require.NoError(t, err, "seed review session")
	job := runCodeReviewPayload{OrgID: org, SessionID: session, RepositoryID: uuid.New(), HeadSHA: workerRiskTierHead}
	policy := workerRiskTierPolicy(workerRiskTierAppID)
	store := db.NewCodeReviewStore(pool)

	first := &fakeRiskTierCheckRuns{runs: []models.CodeReviewCheckRun{workerRiskTierRun(4, workerRiskTierAppID, workerRiskTierHead, "low", "false")}}
	signal, err := resolveCodeReviewRiskTierSignalWith(ctx, store, first, job, policy)
	require.NoError(t, err, "the first attempt should read GitHub and persist the checkpoint under the migrated role contract")
	require.Equal(t, workerRiskTierSignal(models.CodeReviewRiskTierLow, models.CodeReviewUIRenderChangeFalse), signal, "the first attempt should resolve the authoritative payload")

	retry := &fakeRiskTierCheckRuns{runs: []models.CodeReviewCheckRun{workerRiskTierRun(9, workerRiskTierAppID, workerRiskTierHead, "high", "true")}}
	again, err := resolveCodeReviewRiskTierSignalWith(ctx, store, retry, job, policy)
	require.NoError(t, err, "a retried attempt should restore the checkpoint")
	require.Equal(t, signal, again, "a retried attempt should reuse the first answer for the same session and head")
	require.Empty(t, retry.calls, "a retried attempt should not read GitHub again")

	records, err := store.ListPromptRecords(ctx, org, session)
	require.NoError(t, err, "list session prompt records")
	require.Len(t, records, 1, "exactly one checkpoint should exist for the session and head")
	require.Equal(t, codeReviewRiskTierPromptRole, records[0].Role, "the checkpoint should use the risk-tier prompt role")
}
