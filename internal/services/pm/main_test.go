package pm

import (
	"context"
	"os"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	// Legacy unit tests continue exercising the code that will be deleted in
	// PR 3. Production and cross-package tests retain the shutdown default.
	analysisEnabled = true
	os.Exit(m.Run())
}

//nolint:paralleltest // mutates analysisEnabled so production-disabled behavior can be tested without racing legacy PM tests
func TestProductionShutdownGuards(t *testing.T) {
	analysisEnabled = false
	t.Cleanup(func() {
		analysisEnabled = true
	})

	svc := &Service{}
	plan := &Plan{Tasks: []Task{{IssueIDs: []uuid.UUID{uuid.New()}}}}

	_, err := svc.Analyze(context.Background(), uuid.New(), models.PMTriggerManual, nil, nil)
	require.EqualError(t, err, "PM analysis is disabled", "analysis entry should reject work before creating a session or sandbox")
	require.NoError(t, svc.executePlan(context.Background(), uuid.New(), plan, models.OrgSettings{AutonomyLevel: models.AutonomyLevelAutoAll}, nil), "disabled plan execution should be a harmless no-op")
	require.Empty(t, plan.Tasks[0].Status, "disabled plan execution should not mutate or dispatch tasks")
	require.EqualError(t, svc.AnalyzeProject(context.Background(), uuid.New(), uuid.New()), "PM project analysis is disabled", "project analysis entry should reject work")
}
